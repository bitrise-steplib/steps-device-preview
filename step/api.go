package step

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bitrise-io/go-utils/v2/log"
)

const (
	apiTimeout    = 60 * time.Second
	uploadTimeout = 30 * time.Minute

	// The storage PUT is idempotent, so a flaky connection or a 5xx from the bucket is worth
	// another go. The API POSTs are not retried: a lost response to the create-artifact or
	// device-preview call could otherwise leave a duplicate behind.
	uploadAttempts = 3
)

// uploadRetryDelay is a variable so tests do not have to wait it out.
var uploadRetryDelay = 5 * time.Second

type previewOptions struct {
	Platform             string
	DeviceModel          string
	OSVersion            string
	Stack                string
	MachineType          string
	SystemImage          string
	EmulatorRAMMB        int
	EmulatorCores        int
	EmulatorColdBoot     bool
	TTLSeconds           int
	AutoTerminateMinutes int
	PostPRComment        bool
}

// apiClient talks to the Bitrise build API, authenticated with the build's own API token.
type apiClient struct {
	buildURL     string
	token        string
	logger       log.Logger
	apiClient    *http.Client
	uploadClient *http.Client
}

func newAPIClient(buildURL, token string, logger log.Logger) apiClient {
	return apiClient{
		buildURL:     strings.TrimSuffix(buildURL, "/"),
		token:        token,
		logger:       logger,
		apiClient:    &http.Client{Timeout: apiTimeout},
		uploadClient: &http.Client{Timeout: uploadTimeout},
	}
}

// CreateDevicePreview mints a preview link for an artifact of this build.
func (c apiClient) CreateDevicePreview(artifactSlug string, opts previewOptions) (Result, error) {
	form := url.Values{
		"api_token":       {c.token},
		"platform":        {opts.Platform},
		"post_pr_comment": {strconv.FormatBool(opts.PostPRComment)},
	}
	if opts.DeviceModel != "" {
		form.Set("device_model", opts.DeviceModel)
	}
	if opts.OSVersion != "" {
		form.Set("os_version", opts.OSVersion)
	}
	if opts.Stack != "" {
		form.Set("stack_id", opts.Stack)
	}
	if opts.MachineType != "" {
		form.Set("machine_type", opts.MachineType)
	}
	if opts.SystemImage != "" {
		form.Set("system_image", opts.SystemImage)
	}
	if opts.EmulatorRAMMB > 0 {
		form.Set("ram_mb", strconv.Itoa(opts.EmulatorRAMMB))
	}
	if opts.EmulatorCores > 0 {
		form.Set("cores", strconv.Itoa(opts.EmulatorCores))
	}
	if opts.EmulatorColdBoot {
		form.Set("cold_boot", "true")
	}
	if opts.TTLSeconds > 0 {
		form.Set("ttl_seconds", strconv.Itoa(opts.TTLSeconds))
	}
	if opts.AutoTerminateMinutes > 0 {
		form.Set("session_auto_terminate_minutes", strconv.Itoa(opts.AutoTerminateMinutes))
	}

	body, err := c.postForm(fmt.Sprintf("%s/artifacts/%s/device_preview", c.buildURL, artifactSlug), form)
	if err != nil {
		return Result{}, err
	}

	var response struct {
		URL       string `json:"url"`
		ExpiresAt string `json:"expires_at"`
		PRComment *struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"pr_comment"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return Result{}, fmt.Errorf("parse device preview response: %w", err)
	}
	if response.URL == "" {
		return Result{}, fmt.Errorf("the device preview response contained no link")
	}

	result := Result{PreviewURL: response.URL, ExpiresAt: response.ExpiresAt}
	if response.PRComment != nil {
		result.PRCommentStatus = response.PRComment.Status
		result.PRCommentMessage = response.PRComment.Message
	}

	return result, nil
}

// UploadArtifact deploys a file to this build and returns its artifact slug. Used only when the
// file was not already deployed by an earlier Deploy to Bitrise.io Step.
func (c apiClient) UploadArtifact(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	name := filepath.Base(path)
	created, err := c.createArtifact(name, info.Size())
	if err != nil {
		return "", err
	}

	if err := c.putFile(created.UploadURL, path, info.Size()); err != nil {
		return "", err
	}

	if err := c.finishUpload(created.ID); err != nil {
		return "", err
	}

	return created.Slug, nil
}

type createdArtifact struct {
	ID        int    `json:"id"`
	Slug      string `json:"slug"`
	UploadURL string `json:"upload_url"`
}

func (c apiClient) createArtifact(name string, sizeBytes int64) (createdArtifact, error) {
	form := url.Values{
		"api_token":     {c.token},
		"title":         {name},
		"filename":      {name},
		"artifact_type": {"file"},
		// An absent content_type is how the API recognises a pre-2.0.7 Deploy to Bitrise.io Step
		// and rejects the request. Empty but present is what the current Step sends for generic
		// files, and it is what selects GCS-backed storage.
		"content_type":    {""},
		"file_size_bytes": {strconv.FormatInt(sizeBytes, 10)},
	}

	body, err := c.postForm(c.buildURL+"/artifacts.json", form)
	if err != nil {
		return createdArtifact{}, fmt.Errorf("create artifact: %w", err)
	}

	var created createdArtifact
	if err := json.Unmarshal(body, &created); err != nil {
		return createdArtifact{}, fmt.Errorf("parse create artifact response: %w", err)
	}
	if created.UploadURL == "" || created.Slug == "" {
		return createdArtifact{}, fmt.Errorf("the create artifact response was missing an upload URL or slug")
	}

	return created, nil
}

// putFile uploads the file to the signed storage URL, retrying transient failures.
func (c apiClient) putFile(uploadURL, path string, sizeBytes int64) error {
	name := filepath.Base(path)

	var lastErr error
	for attempt := 1; attempt <= uploadAttempts; attempt++ {
		if attempt > 1 {
			c.logger.Warnf("Upload of %s failed (%s), retrying in %s (attempt %d of %d).", name, lastErr, uploadRetryDelay, attempt, uploadAttempts)
			time.Sleep(uploadRetryDelay)
		}

		err := c.putFileOnce(uploadURL, path, sizeBytes)
		if err == nil {
			return nil
		}

		var transient *transientError
		if !errors.As(err, &transient) {
			return err
		}
		lastErr = err
	}

	return fmt.Errorf("upload %s: giving up after %d attempts: %w", name, uploadAttempts, lastErr)
}

// transientError marks an upload failure that is worth retrying.
type transientError struct {
	err error
}

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

func (c apiClient) putFileOnce(uploadURL, path string, sizeBytes int64) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			c.logger.Warnf("Failed to close %s: %s", path, err)
		}
	}()

	// A nil body is what makes net/http send Content-Length: 0 rather than omitting it, which an
	// empty file's signed URL still expects. See golang/go#20257.
	var body io.Reader
	if sizeBytes > 0 {
		body = file
	}

	request, err := http.NewRequest(http.MethodPut, uploadURL, body)
	if err != nil {
		return fmt.Errorf("build upload request: %w", err)
	}
	// Storage rejects a chunked PUT, so the length has to be explicit.
	request.ContentLength = sizeBytes
	// Part of what the GCS signed URL is signed over.
	request.Header.Set("X-Upload-Content-Length", strconv.FormatInt(sizeBytes, 10))

	response, err := c.uploadClient.Do(request)
	if err != nil {
		return &transientError{err: fmt.Errorf("upload %s: %w", filepath.Base(path), err)}
	}
	defer c.closeBody(response)

	if response.StatusCode >= 200 && response.StatusCode <= 299 {
		return nil
	}

	err = fmt.Errorf("upload %s: storage returned %s", filepath.Base(path), response.Status)
	if isTransientStatus(response.StatusCode) {
		return &transientError{err: err}
	}

	return err
}

// isTransientStatus is true for responses where the same request may well succeed a moment
// later. A 4xx from a signed URL means the signature or the request is wrong, not the moment.
func isTransientStatus(status int) bool {
	return status >= 500 || status == http.StatusTooManyRequests || status == http.StatusRequestTimeout
}

func (c apiClient) finishUpload(artifactID int) error {
	form := url.Values{"api_token": {c.token}}

	if _, err := c.postForm(fmt.Sprintf("%s/artifacts/%d/finish_upload.json", c.buildURL, artifactID), form); err != nil {
		return fmt.Errorf("finish upload: %w", err)
	}

	return nil
}

func (c apiClient) postForm(endpoint string, form url.Values) ([]byte, error) {
	response, err := c.apiClient.PostForm(endpoint, form)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", redactedURL(endpoint), err)
	}
	defer c.closeBody(response)

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read response from %s: %w", redactedURL(endpoint), err)
	}

	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("%s returned %s: %s", redactedURL(endpoint), response.Status, errorMessage(body))
	}

	return body, nil
}

func (c apiClient) closeBody(response *http.Response) {
	if err := response.Body.Close(); err != nil {
		c.logger.Debugf("Failed to close response body: %s", err)
	}
}

// errorMessage prefers the API's own message over the raw body.
func errorMessage(body []byte) string {
	var payload struct {
		ErrorMsg string `json:"error_msg"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		if payload.ErrorMsg != "" {
			return payload.ErrorMsg
		}
		if payload.Message != "" {
			return payload.Message
		}
	}

	return strings.TrimSpace(string(body))
}

// redactedURL drops the query string, which on storage URLs carries the signature.
func redactedURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "the request URL"
	}
	parsed.RawQuery = ""

	return parsed.String()
}

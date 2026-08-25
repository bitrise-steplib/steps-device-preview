package step

import (
	"encoding/json"
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
)

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
	buildURL string
	token    string
	logger   log.Logger
	client   *http.Client
}

func newAPIClient(buildURL, token string, logger log.Logger) apiClient {
	return apiClient{
		buildURL: strings.TrimSuffix(buildURL, "/"),
		token:    token,
		logger:   logger,
		client:   &http.Client{Timeout: uploadTimeout},
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

func (c apiClient) putFile(uploadURL, path string, sizeBytes int64) error {
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

	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("upload %s: %w", filepath.Base(path), err)
	}
	defer c.closeBody(response)

	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("upload %s: storage returned %s", filepath.Base(path), response.Status)
	}

	return nil
}

func (c apiClient) finishUpload(artifactID int) error {
	form := url.Values{"api_token": {c.token}}

	if _, err := c.postForm(fmt.Sprintf("%s/artifacts/%d/finish_upload.json", c.buildURL, artifactID), form); err != nil {
		return fmt.Errorf("finish upload: %w", err)
	}

	return nil
}

func (c apiClient) postForm(endpoint string, form url.Values) ([]byte, error) {
	client := &http.Client{Timeout: apiTimeout}

	response, err := client.PostForm(endpoint, form)
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

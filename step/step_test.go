package step

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bitrise-io/go-steputils/v2/stepconf"
	"github.com/bitrise-io/go-utils/v2/env"
	"github.com/bitrise-io/go-utils/v2/log"
	"github.com/stretchr/testify/require"
)

func TestProcessConfig(t *testing.T) {
	appPath := writeAppZip(t, "Fruta.app.zip", "Fruta.app/Info.plist", simulatorPlist)

	tests := []struct {
		name      string
		overrides map[string]string
		want      func(t *testing.T, config Config)
		wantErr   string
	}{
		{
			name: "defaults leave everything to the backend",
			want: func(t *testing.T, config Config) {
				require.Equal(t, appPath, config.AppPath)
				require.Empty(t, config.Platform)
				require.Zero(t, config.LinkTTLSeconds)
				require.Zero(t, config.AutoTerminateMinutes)
				require.Zero(t, config.EmulatorRAMMB)
				require.Zero(t, config.EmulatorCores)
				require.False(t, config.EmulatorColdBoot)
				require.False(t, config.PostPRComment)
				require.Equal(t, "secret", config.BuildAPIToken)
			},
		},
		{
			name:      "auto platform means detect",
			overrides: map[string]string{"platform": "auto"},
			want:      func(t *testing.T, config Config) { require.Empty(t, config.Platform) },
		},
		{
			name:      "an empty platform, as older Workflows set it, still means detect",
			overrides: map[string]string{"platform": ""},
			want:      func(t *testing.T, config Config) { require.Empty(t, config.Platform) },
		},
		{
			name:      "a pinned platform is kept",
			overrides: map[string]string{"platform": "android"},
			want:      func(t *testing.T, config Config) { require.Equal(t, PlatformAndroid, config.Platform) },
		},
		{
			name:      "an unknown platform is rejected",
			overrides: map[string]string{"platform": "tvos"},
			wantErr:   `platform must be "auto", "ios" or "android", got "tvos"`,
		},
		{
			name:      "a missing app is rejected before anything else happens",
			overrides: map[string]string{"app_path": filepath.Join(t.TempDir(), "missing.apk")},
			wantErr:   "is not readable",
		},
		{
			name:      "the link lifetime is converted to seconds",
			overrides: map[string]string{"link_ttl_hours": "48"},
			want:      func(t *testing.T, config Config) { require.Equal(t, 48*3600, config.LinkTTLSeconds) },
		},
		{
			name:      "a link lifetime above the maximum is rejected",
			overrides: map[string]string{"link_ttl_hours": "96"},
			wantErr:   "link_ttl_hours must be at most 72, got 96",
		},
		{
			name:      "a non-numeric link lifetime is rejected",
			overrides: map[string]string{"link_ttl_hours": "two days"},
			wantErr:   `link_ttl_hours must be a whole number of hours, got "two days"`,
		},
		{
			name:      "emulator sizing is parsed",
			overrides: map[string]string{"emulator_ram_mb": "4096", "emulator_cores": "4", "emulator_cold_boot": "true"},
			want: func(t *testing.T, config Config) {
				require.Equal(t, 4096, config.EmulatorRAMMB)
				require.Equal(t, 4, config.EmulatorCores)
				require.True(t, config.EmulatorColdBoot)
			},
		},
		{
			name:      "emulator sizing has to be a number",
			overrides: map[string]string{"emulator_ram_mb": "4GB"},
			wantErr:   `emulator_ram_mb must be a whole number, got "4GB"`,
		},
		{
			name:      "the auto-terminate window has to be positive",
			overrides: map[string]string{"auto_terminate_minutes": "0"},
			wantErr:   "auto_terminate_minutes must be positive, got 0",
		},
		{
			name:      "a warm pool is kept, trimmed",
			overrides: map[string]string{"warm_pool_id": " ios-devs "},
			want:      func(t *testing.T, config Config) { require.Equal(t, "ios-devs", config.WarmPoolID) },
		},
		{
			name:      "a warm pool fixes the device and the machine",
			overrides: map[string]string{"warm_pool_id": "ios-devs", "device_model": "iPhone 15", "machine_type": "g2.mac.large", "emulator_cores": "4"},
			wantErr:   "device_model, machine_type, emulator_cores cannot be used with warm_pool_id: the warm pool fixes the device and the machine",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputs := map[string]string{
				"app_path":           appPath,
				"platform":           "auto",
				"emulator_cold_boot": "false",
				"post_pr_comment":    "false",
				"verbose":            "false",
				"build_url":          "https://app.bitrise.io/build/abc123",
				"build_api_token":    "secret",
			}
			for key, value := range tt.overrides {
				inputs[key] = value
			}
			for key, value := range inputs {
				t.Setenv(key, value)
			}

			previewStep := New(log.NewLogger(), stepconf.NewInputParser(env.NewRepository()), nil)

			config, err := previewStep.ProcessConfig()

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			tt.want(t, config)
		})
	}
}

func TestRun(t *testing.T) {
	uploadRetryDelay = 0

	const deployedURLMap = "Fruta.app.zip=>https://app.bitrise.io/artifact/deployed-1/download"

	t.Run("reuses an artifact an earlier Deploy Step uploaded", func(t *testing.T) {
		api := newFakeBuildAPI(t)
		appPath := writeAppZip(t, "Fruta.app.zip", "Fruta.app/Info.plist", simulatorPlist)

		result, err := newTestStep().Run(Config{
			AppPath:                 appPath,
			PermanentDownloadURLMap: deployedURLMap,
			BuildURL:                api.server.URL,
			BuildAPIToken:           "token",
		})

		require.NoError(t, err)
		require.Equal(t, "https://app.bitrise.io/dev-environments/ws/device-preview/abc", result.PreviewURL)
		require.Equal(t, "2026-09-19T12:00:00Z", result.ExpiresAt)
		require.Empty(t, result.PRCommentStatus)

		require.Equal(t, []string{"POST /artifacts/deployed-1/device_preview"}, api.calls())
		form := api.requests[0].form
		require.Equal(t, "token", form.Get("api_token"))
		require.Equal(t, PlatformIOS, form.Get("platform"))
		require.Equal(t, "false", form.Get("post_pr_comment"))
		for _, key := range []string{"device_model", "os_version", "stack_id", "machine_type", "system_image", "ram_mb", "cores", "cold_boot", "ttl_seconds", "session_auto_terminate_minutes"} {
			require.False(t, form.Has(key), "%s should be left to the backend's default", key)
		}
	})

	t.Run("uploads the app itself when no earlier Step deployed it, riding out a storage hiccup", func(t *testing.T) {
		api := newFakeBuildAPI(t)
		api.uploadFailures = 1
		apkPath := writeFile(t, "app-debug.apk", "not really an apk")

		result, err := newTestStep().Run(Config{
			AppPath:       apkPath,
			BuildURL:      api.server.URL,
			BuildAPIToken: "token",
		})

		require.NoError(t, err)
		require.Equal(t, "https://app.bitrise.io/dev-environments/ws/device-preview/abc", result.PreviewURL)
		require.Equal(t, []string{
			"POST /artifacts.json",
			"PUT /upload",
			"PUT /upload",
			"POST /artifacts/42/finish_upload.json",
			"POST /artifacts/uploaded-42/device_preview",
		}, api.calls())

		create := api.requests[0].form
		require.Equal(t, "app-debug.apk", create.Get("filename"))
		require.Equal(t, "app-debug.apk", create.Get("title"))
		require.Equal(t, "file", create.Get("artifact_type"))
		require.True(t, create.Has("content_type"), "content_type has to be present, even if empty")
		require.Equal(t, "17", create.Get("file_size_bytes"))

		require.Equal(t, "not really an apk", api.uploadedBody)
		require.Equal(t, PlatformAndroid, api.requests[4].form.Get("platform"))
	})

	t.Run("does not retry an upload the storage rejected", func(t *testing.T) {
		api := newFakeBuildAPI(t)
		api.uploadStatus = http.StatusForbidden
		apkPath := writeFile(t, "app-debug.apk", "not really an apk")

		_, err := newTestStep().Run(Config{
			AppPath:       apkPath,
			BuildURL:      api.server.URL,
			BuildAPIToken: "token",
		})

		require.ErrorContains(t, err, "storage returned 403 Forbidden")
		require.Equal(t, []string{"POST /artifacts.json", "PUT /upload"}, api.calls())
	})

	t.Run("gives up after repeated transient upload failures", func(t *testing.T) {
		api := newFakeBuildAPI(t)
		api.uploadFailures = uploadAttempts
		apkPath := writeFile(t, "app-debug.apk", "not really an apk")

		_, err := newTestStep().Run(Config{
			AppPath:       apkPath,
			BuildURL:      api.server.URL,
			BuildAPIToken: "token",
		})

		require.ErrorContains(t, err, fmt.Sprintf("giving up after %d attempts", uploadAttempts))
		require.ErrorContains(t, err, "storage returned 503")
		require.Len(t, api.calls(), 1+uploadAttempts)
	})

	t.Run("passes the device and lifetime options through under the API's names", func(t *testing.T) {
		api := newFakeBuildAPI(t)
		apkPath := writeFile(t, "app-debug.apk", "not really an apk")

		_, err := newTestStep().Run(Config{
			AppPath:                 apkPath,
			DeviceModel:             "pixel_7",
			Stack:                   "linux-docker-android-22.04",
			MachineType:             "g2.linux.x-large",
			SystemImage:             "system-images;android-34;google_apis;x86_64",
			EmulatorRAMMB:           4096,
			EmulatorCores:           4,
			EmulatorColdBoot:        true,
			LinkTTLSeconds:          7200,
			AutoTerminateMinutes:    30,
			PostPRComment:           true,
			PermanentDownloadURLMap: "app-debug.apk=>https://app.bitrise.io/artifact/deployed-2/download",
			BuildURL:                api.server.URL,
			BuildAPIToken:           "token",
		})

		require.NoError(t, err)
		require.Equal(t, []string{"POST /artifacts/deployed-2/device_preview"}, api.calls())
		form := api.requests[0].form
		require.Equal(t, url.Values{
			"api_token":                      {"token"},
			"platform":                       {PlatformAndroid},
			"post_pr_comment":                {"true"},
			"device_model":                   {"pixel_7"},
			"stack_id":                       {"linux-docker-android-22.04"},
			"machine_type":                   {"g2.linux.x-large"},
			"system_image":                   {"system-images;android-34;google_apis;x86_64"},
			"ram_mb":                         {"4096"},
			"cores":                          {"4"},
			"cold_boot":                      {"true"},
			"ttl_seconds":                    {"7200"},
			"session_auto_terminate_minutes": {"30"},
		}, form)
	})

	t.Run("passes the warm pool through, with the platform the pool must match", func(t *testing.T) {
		api := newFakeBuildAPI(t)
		apkPath := writeFile(t, "app-debug.apk", "not really an apk")

		_, err := newTestStep().Run(Config{
			AppPath:                 apkPath,
			WarmPoolID:              "android-reviewers",
			PermanentDownloadURLMap: "app-debug.apk=>https://app.bitrise.io/artifact/deployed-2/download",
			BuildURL:                api.server.URL,
			BuildAPIToken:           "token",
		})

		require.NoError(t, err)
		require.Equal(t, []string{"POST /artifacts/deployed-2/device_preview"}, api.calls())
		require.Equal(t, url.Values{
			"api_token":       {"token"},
			"platform":        {PlatformAndroid},
			"post_pr_comment": {"false"},
			"warm_pool_id":    {"android-reviewers"},
		}, api.requests[0].form)
	})

	t.Run("reports a pull request comment that did not land without failing", func(t *testing.T) {
		api := newFakeBuildAPI(t)
		api.previewResponse = `{"url":"https://example.com/p/abc","expires_at":"2026-09-19T12:00:00Z","pr_comment":{"status":"skipped","message":"build is not a pull request build"}}`
		appPath := writeAppZip(t, "Fruta.app.zip", "Fruta.app/Info.plist", simulatorPlist)

		result, err := newTestStep().Run(Config{
			AppPath:                 appPath,
			PostPRComment:           true,
			PermanentDownloadURLMap: deployedURLMap,
			BuildURL:                api.server.URL,
			BuildAPIToken:           "token",
		})

		require.NoError(t, err)
		require.Equal(t, "https://example.com/p/abc", result.PreviewURL)
		require.Equal(t, "skipped", result.PRCommentStatus)
		require.Equal(t, "build is not a pull request build", result.PRCommentMessage)
	})

	t.Run("surfaces the API's own error message", func(t *testing.T) {
		api := newFakeBuildAPI(t)
		api.previewStatus = http.StatusBadRequest
		api.previewResponse = `{"error_msg":"machine type g2.linux.medium is too small to run a preview device"}`
		appPath := writeAppZip(t, "Fruta.app.zip", "Fruta.app/Info.plist", simulatorPlist)

		_, err := newTestStep().Run(Config{
			AppPath:                 appPath,
			PermanentDownloadURLMap: deployedURLMap,
			BuildURL:                api.server.URL,
			BuildAPIToken:           "token",
		})

		require.ErrorContains(t, err, "400 Bad Request: machine type g2.linux.medium is too small to run a preview device")
	})

	t.Run("a response without a link is an error", func(t *testing.T) {
		api := newFakeBuildAPI(t)
		api.previewResponse = `{"expires_at":"2026-09-19T12:00:00Z"}`
		appPath := writeAppZip(t, "Fruta.app.zip", "Fruta.app/Info.plist", simulatorPlist)

		_, err := newTestStep().Run(Config{
			AppPath:                 appPath,
			PermanentDownloadURLMap: deployedURLMap,
			BuildURL:                api.server.URL,
			BuildAPIToken:           "token",
		})

		require.ErrorContains(t, err, "contained no link")
	})

	t.Run("rejects emulator options for an iOS app before talking to the API", func(t *testing.T) {
		api := newFakeBuildAPI(t)
		appPath := writeAppZip(t, "Fruta.app.zip", "Fruta.app/Info.plist", simulatorPlist)

		_, err := newTestStep().Run(Config{
			AppPath:       appPath,
			EmulatorCores: 4,
			BuildURL:      api.server.URL,
			BuildAPIToken: "token",
		})

		require.ErrorContains(t, err, "cannot be used with an iOS app")
		require.Empty(t, api.calls())
	})

	t.Run("rejects os_version for an Android app before talking to the API", func(t *testing.T) {
		api := newFakeBuildAPI(t)
		apkPath := writeFile(t, "app-debug.apk", "not really an apk")

		_, err := newTestStep().Run(Config{
			AppPath:       apkPath,
			OSVersion:     "17.5",
			BuildURL:      api.server.URL,
			BuildAPIToken: "token",
		})

		require.ErrorContains(t, err, "os_version selects the iOS Simulator runtime and cannot be used with an Android app")
		require.Empty(t, api.calls())
	})

	t.Run("removes the zip it made from a .app directory", func(t *testing.T) {
		api := newFakeBuildAPI(t)
		scratch := t.TempDir()
		t.Setenv("TMPDIR", scratch)
		appDir := filepath.Join(t.TempDir(), "Fruta.app")
		require.NoError(t, os.MkdirAll(appDir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(appDir, "Info.plist"), []byte(simulatorPlist), 0o600))

		_, err := newTestStep().Run(Config{
			AppPath:                 appDir,
			PermanentDownloadURLMap: deployedURLMap,
			BuildURL:                api.server.URL,
			BuildAPIToken:           "token",
		})

		require.NoError(t, err)
		leftovers, err := os.ReadDir(scratch)
		require.NoError(t, err)
		require.Empty(t, leftovers, "the temporary zip should be gone once the link exists")
	})
}

func TestExportOutputs(t *testing.T) {
	t.Run("exports the link and its expiry", func(t *testing.T) {
		exporter := &fakeExporter{}

		err := New(log.NewLogger(), nil, exporter).ExportOutputs(Result{PreviewURL: "https://example.com/p/abc", ExpiresAt: "2026-09-19T12:00:00Z"})

		require.NoError(t, err)
		require.Equal(t, map[string]string{
			"BITRISE_DEVICE_PREVIEW_URL":        "https://example.com/p/abc",
			"BITRISE_DEVICE_PREVIEW_EXPIRES_AT": "2026-09-19T12:00:00Z",
		}, exporter.exported)
	})

	t.Run("skips an expiry the API did not return", func(t *testing.T) {
		exporter := &fakeExporter{}

		err := New(log.NewLogger(), nil, exporter).ExportOutputs(Result{PreviewURL: "https://example.com/p/abc"})

		require.NoError(t, err)
		require.Equal(t, map[string]string{"BITRISE_DEVICE_PREVIEW_URL": "https://example.com/p/abc"}, exporter.exported)
	})

	t.Run("an envman failure is reported", func(t *testing.T) {
		exporter := &fakeExporter{err: fmt.Errorf("envman is not on the PATH")}

		err := New(log.NewLogger(), nil, exporter).ExportOutputs(Result{PreviewURL: "https://example.com/p/abc"})

		require.EqualError(t, err, "export BITRISE_DEVICE_PREVIEW_URL: envman is not on the PATH")
	})
}

func TestPositiveInt(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    int
		wantErr string
	}{
		{name: "empty means the default", raw: "", want: 0},
		{name: "a plain number parses", raw: "2048", want: 2048},
		{name: "not a number", raw: "2GB", wantErr: `emulator_ram_mb must be a whole number, got "2GB"`},
		{name: "zero is not a usable value", raw: "0", wantErr: "emulator_ram_mb must be positive, got 0"},
		{name: "negative", raw: "-4", wantErr: "emulator_ram_mb must be positive, got -4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := positiveInt("emulator_ram_mb", tt.raw)

			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestHasEmulatorConfig(t *testing.T) {
	require.False(t, hasEmulatorConfig(Config{}))
	require.True(t, hasEmulatorConfig(Config{SystemImage: "system-images;android-34;google_apis;x86_64"}))
	require.True(t, hasEmulatorConfig(Config{EmulatorRAMMB: 4096}))
	require.True(t, hasEmulatorConfig(Config{EmulatorCores: 4}))
	require.True(t, hasEmulatorConfig(Config{EmulatorColdBoot: true}))
}

func newTestStep() DevicePreview {
	return New(log.NewLogger(), nil, nil)
}

type fakeExporter struct {
	exported map[string]string
	err      error
}

func (e *fakeExporter) ExportOutput(key, value string) error {
	if e.err != nil {
		return e.err
	}
	if e.exported == nil {
		e.exported = map[string]string{}
	}
	e.exported[key] = value

	return nil
}

type recordedRequest struct {
	method string
	path   string
	form   url.Values
}

// fakeBuildAPI stands in for the Bitrise build API and the storage bucket behind its signed
// upload URLs.
type fakeBuildAPI struct {
	t      *testing.T
	server *httptest.Server

	requests     []recordedRequest
	uploadedBody string

	// Number of 503s the storage returns before accepting the upload.
	uploadFailures int
	// A fixed status for every upload attempt; 0 means accept.
	uploadStatus int

	previewStatus   int
	previewResponse string
}

func newFakeBuildAPI(t *testing.T) *fakeBuildAPI {
	t.Helper()

	api := &fakeBuildAPI{
		t:               t,
		previewStatus:   http.StatusOK,
		previewResponse: `{"url":"https://app.bitrise.io/dev-environments/ws/device-preview/abc","expires_at":"2026-09-19T12:00:00Z"}`,
	}
	api.server = httptest.NewServer(http.HandlerFunc(api.handle))
	t.Cleanup(api.server.Close)

	return api
}

func (api *fakeBuildAPI) calls() []string {
	var calls []string
	for _, request := range api.requests {
		calls = append(calls, request.method+" "+request.path)
	}

	return calls
}

func (api *fakeBuildAPI) handle(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	require.NoError(api.t, err)

	recorded := recordedRequest{method: r.Method, path: r.URL.Path}
	if r.Method == http.MethodPost {
		recorded.form, err = url.ParseQuery(string(body))
		require.NoError(api.t, err)
	}
	api.requests = append(api.requests, recorded)

	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/artifacts.json":
		api.respond(w, http.StatusOK, map[string]any{"id": 42, "slug": "uploaded-42", "upload_url": api.server.URL + "/upload?signature=secret"})
	case r.Method == http.MethodPut && r.URL.Path == "/upload":
		switch {
		case api.uploadStatus != 0:
			w.WriteHeader(api.uploadStatus)
		case api.uploadFailures > 0:
			api.uploadFailures--
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			api.uploadedBody = string(body)
			w.WriteHeader(http.StatusOK)
		}
	case r.Method == http.MethodPost && r.URL.Path == "/artifacts/42/finish_upload.json":
		api.respond(w, http.StatusOK, map[string]any{})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/device_preview"):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(api.previewStatus)
		_, err := io.WriteString(w, api.previewResponse)
		require.NoError(api.t, err)
	default:
		api.t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (api *fakeBuildAPI) respond(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	require.NoError(api.t, json.NewEncoder(w).Encode(payload))
}

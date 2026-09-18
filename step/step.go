package step

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/bitrise-io/go-steputils/v2/stepconf"
	"github.com/bitrise-io/go-utils/v2/log"
)

const (
	previewURLEnvKey       = "BITRISE_DEVICE_PREVIEW_URL"
	previewExpiresAtEnvKey = "BITRISE_DEVICE_PREVIEW_EXPIRES_AT"

	// RDE rejects anything above this instead of clamping it, so catch it locally where the
	// message can name the input.
	maxLinkTTLHours = 72
)

// Input is the raw Step configuration, as the Bitrise CLI provides it.
type Input struct {
	AppPath     string `env:"app_path,required"`
	Platform    string `env:"platform"`
	DeviceModel string `env:"device_model"`
	OSVersion   string `env:"os_version"`

	Stack            string `env:"stack"`
	MachineType      string `env:"machine_type"`
	SystemImage      string `env:"system_image"`
	EmulatorRAMMB    string `env:"emulator_ram_mb"`
	EmulatorCores    string `env:"emulator_cores"`
	EmulatorColdBoot bool   `env:"emulator_cold_boot,opt[true,false]"`

	LinkTTLHours         string `env:"link_ttl_hours"`
	AutoTerminateMinutes string `env:"auto_terminate_minutes"`
	PostPRComment        bool   `env:"post_pr_comment,opt[true,false]"`

	PermanentDownloadURLMap string `env:"permanent_download_url_map"`

	BuildURL      string          `env:"build_url,required"`
	BuildAPIToken stepconf.Secret `env:"build_api_token,required"`

	Verbose bool `env:"verbose,opt[true,false]"`
}

// Config is the validated Step configuration.
type Config struct {
	AppPath string
	// Empty means "detect it from the app".
	Platform    string
	DeviceModel string
	OSVersion   string

	Stack            string
	MachineType      string
	SystemImage      string
	EmulatorRAMMB    int
	EmulatorCores    int
	EmulatorColdBoot bool

	LinkTTLSeconds       int
	AutoTerminateMinutes int
	PostPRComment        bool

	PermanentDownloadURLMap string

	BuildURL      string
	BuildAPIToken string
}

// Comment statuses the API reports back when a pull request comment was asked for.
const (
	PRCommentPosted = "posted"
)

// Result is what the Step exports once a preview link exists.
type Result struct {
	PreviewURL string
	ExpiresAt  string

	// Empty unless a pull request comment was requested.
	PRCommentStatus  string
	PRCommentMessage string
}

// OutputExporter exposes values to the Steps that run after this one.
type OutputExporter interface {
	ExportOutput(key, value string) error
}

// DevicePreview creates a device preview link for an app built in this build.
type DevicePreview struct {
	logger      log.Logger
	inputParser stepconf.InputParser
	exporter    OutputExporter
}

// New ...
func New(logger log.Logger, inputParser stepconf.InputParser, exporter OutputExporter) DevicePreview {
	return DevicePreview{logger: logger, inputParser: inputParser, exporter: exporter}
}

// ProcessConfig ...
func (s DevicePreview) ProcessConfig() (Config, error) {
	var input Input
	if err := s.inputParser.Parse(&input); err != nil {
		return Config{}, err
	}

	stepconf.Print(input)
	s.logger.EnableDebugLog(input.Verbose)

	platform, err := normalisePlatform(input.Platform)
	if err != nil {
		return Config{}, err
	}

	if _, err := os.Stat(input.AppPath); err != nil {
		return Config{}, fmt.Errorf("app_path %s is not readable: %w", input.AppPath, err)
	}

	ttlSeconds, err := linkTTLSeconds(input.LinkTTLHours)
	if err != nil {
		return Config{}, err
	}

	ramMB, err := positiveInt("emulator_ram_mb", input.EmulatorRAMMB)
	if err != nil {
		return Config{}, err
	}
	cores, err := positiveInt("emulator_cores", input.EmulatorCores)
	if err != nil {
		return Config{}, err
	}
	autoTerminateMinutes, err := positiveInt("auto_terminate_minutes", input.AutoTerminateMinutes)
	if err != nil {
		return Config{}, err
	}

	return Config{
		AppPath:                 input.AppPath,
		Platform:                platform,
		DeviceModel:             input.DeviceModel,
		OSVersion:               input.OSVersion,
		Stack:                   input.Stack,
		MachineType:             input.MachineType,
		SystemImage:             input.SystemImage,
		EmulatorRAMMB:           ramMB,
		EmulatorCores:           cores,
		EmulatorColdBoot:        input.EmulatorColdBoot,
		LinkTTLSeconds:          ttlSeconds,
		AutoTerminateMinutes:    autoTerminateMinutes,
		PostPRComment:           input.PostPRComment,
		PermanentDownloadURLMap: input.PermanentDownloadURLMap,
		BuildURL:                input.BuildURL,
		BuildAPIToken:           string(input.BuildAPIToken),
	}, nil
}

// Run prepares the app, makes sure it exists as a build artifact, and mints the preview link.
func (s DevicePreview) Run(config Config) (Result, error) {
	artifact, err := s.prepareArtifact(config.AppPath, config.Platform)
	if err != nil {
		return Result{}, err
	}
	if artifact.TempDir != "" {
		defer func() {
			if err := os.RemoveAll(artifact.TempDir); err != nil {
				s.logger.Debugf("Failed to remove %s: %s", artifact.TempDir, err)
			}
		}()
	}
	s.logger.Printf("Platform: %s", artifact.Platform)

	// Catch these before the upload — the backend would reject them anyway, but only after the
	// artifact round-trip, and with the API's field names instead of the Step's input names.
	if err := verifyDeviceConfig(artifact.Platform, config); err != nil {
		return Result{}, err
	}

	client := newAPIClient(config.BuildURL, config.BuildAPIToken, s.logger)

	fileName := filepath.Base(artifact.Path)
	slug := ArtifactSlugFor(config.PermanentDownloadURLMap, fileName)
	if slug != "" {
		s.logger.Donef("Found %s among this build's artifacts (%s).", fileName, slug)
	} else {
		s.logger.Printf("%s was not deployed by an earlier Deploy to Bitrise.io Step, uploading it now.", fileName)

		slug, err = client.UploadArtifact(artifact.Path)
		if err != nil {
			return Result{}, fmt.Errorf("upload %s: %w", artifact.Path, err)
		}
		s.logger.Donef("Uploaded as artifact %s.", slug)
	}

	s.logger.Println()
	s.logger.Infof("Creating the device preview link")

	result, err := client.CreateDevicePreview(slug, previewOptions{
		Platform:             artifact.Platform,
		DeviceModel:          config.DeviceModel,
		OSVersion:            config.OSVersion,
		Stack:                config.Stack,
		MachineType:          config.MachineType,
		SystemImage:          config.SystemImage,
		EmulatorRAMMB:        config.EmulatorRAMMB,
		EmulatorCores:        config.EmulatorCores,
		EmulatorColdBoot:     config.EmulatorColdBoot,
		TTLSeconds:           config.LinkTTLSeconds,
		AutoTerminateMinutes: config.AutoTerminateMinutes,
		PostPRComment:        config.PostPRComment,
	})
	if err != nil {
		return Result{}, err
	}

	s.logger.Donef("Device preview: %s", result.PreviewURL)
	if result.ExpiresAt != "" {
		s.logger.Printf("The link expires at %s.", result.ExpiresAt)
	}

	// The link is the point, so a comment that did not land is worth a warning but not a failure.
	switch result.PRCommentStatus {
	case "":
	case PRCommentPosted:
		s.logger.Donef("Posted the link as a pull request comment.")
	default:
		s.logger.Warnf("The link was not posted as a pull request comment: %s", commentProblem(result))
	}

	return result, nil
}

// ExportOutputs ...
func (s DevicePreview) ExportOutputs(result Result) error {
	outputs := []struct{ key, value string }{
		{previewURLEnvKey, result.PreviewURL},
		{previewExpiresAtEnvKey, result.ExpiresAt},
	}

	for _, output := range outputs {
		if output.value == "" {
			continue
		}
		if err := s.exporter.ExportOutput(output.key, output.value); err != nil {
			return fmt.Errorf("export %s: %w", output.key, err)
		}
		s.logger.Donef("Exported %s", output.key)
	}

	return nil
}

func commentProblem(result Result) string {
	if result.PRCommentMessage != "" {
		return result.PRCommentMessage
	}

	return result.PRCommentStatus
}

// normalisePlatform maps the `platform` input onto the wire value; "auto" and "" both mean
// "detect it from the app".
func normalisePlatform(raw string) (string, error) {
	switch raw {
	case "", PlatformAuto:
		return "", nil
	case PlatformIOS, PlatformAndroid:
		return raw, nil
	default:
		return "", fmt.Errorf("platform must be %q, %q or %q, got %q", PlatformAuto, PlatformIOS, PlatformAndroid, raw)
	}
}

// verifyDeviceConfig rejects device options that do not apply to the app's platform.
func verifyDeviceConfig(platform string, config Config) error {
	switch platform {
	case PlatformIOS:
		if hasEmulatorConfig(config) {
			return fmt.Errorf("system_image, emulator_ram_mb, emulator_cores and emulator_cold_boot configure the Android emulator and cannot be used with an iOS app")
		}
	case PlatformAndroid:
		if config.OSVersion != "" {
			return fmt.Errorf("os_version selects the iOS Simulator runtime and cannot be used with an Android app — set system_image to pick the Android version instead")
		}
	}

	return nil
}

func hasEmulatorConfig(config Config) bool {
	return config.SystemImage != "" || config.EmulatorRAMMB > 0 || config.EmulatorCores > 0 || config.EmulatorColdBoot
}

// positiveInt parses an optional numeric input; empty means "use the default", which is 0 on the
// wire. The value's bounds are the backend's to enforce — its message names them.
func positiveInt(name, raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number, got %q", name, raw)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be positive, got %d", name, value)
	}

	return value, nil
}

func linkTTLSeconds(rawHours string) (int, error) {
	if rawHours == "" {
		return 0, nil // 0 means "use the deployment default", which is 24h
	}

	hours, err := strconv.Atoi(rawHours)
	if err != nil {
		return 0, fmt.Errorf("link_ttl_hours must be a whole number of hours, got %q", rawHours)
	}
	if hours <= 0 {
		return 0, fmt.Errorf("link_ttl_hours must be positive, got %d", hours)
	}
	if hours > maxLinkTTLHours {
		return 0, fmt.Errorf("link_ttl_hours must be at most %d, got %d", maxLinkTTLHours, hours)
	}

	return hours * 3600, nil
}

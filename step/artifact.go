package step

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/bitrise-io/go-utils/ziputil"
	"howett.net/plist"
)

// Supported preview platforms, matching the RDE API's `device_spec.platform`, plus the
// `platform` input's "work it out from the app" value.
const (
	PlatformAuto    = "auto"
	PlatformIOS     = "ios"
	PlatformAndroid = "android"
)

// Artifact is an app ready to be handed to RDE: a single uploadable file plus the platform
// its contents target.
type Artifact struct {
	Path     string
	Platform string
	// Set when Path was created by the Step and should be removed once it is uploaded.
	TempDir string
}

// prepareArtifact turns the user's `app_path` into an uploadable file and works out which
// platform it targets. A declared platform is honoured but still verified — a link to something
// the device cannot install is worse than a clear failure here.
func (s DevicePreview) prepareArtifact(appPath, declaredPlatform string) (Artifact, error) {
	info, err := os.Stat(appPath)
	if err != nil {
		return Artifact{}, fmt.Errorf("read app_path %s: %w", appPath, err)
	}

	if info.IsDir() {
		return s.prepareAppDirectory(appPath, declaredPlatform)
	}

	switch strings.ToLower(filepath.Ext(appPath)) {
	case ".apk":
		return Artifact{Path: appPath, Platform: PlatformAndroid}, verifyDeclaredPlatform(declaredPlatform, PlatformAndroid)
	case ".aab":
		return Artifact{}, fmt.Errorf("%s is an Android App Bundle, which cannot be installed on a device as-is — build an APK instead", filepath.Base(appPath))
	case ".ipa":
		return Artifact{}, fmt.Errorf("%s is a device build; a Simulator cannot run it — use a simulator build, for example $BITRISE_APP_DIR_PATH.zip from the Xcode build for simulator Step", filepath.Base(appPath))
	case ".zip":
		if err := verifyZippedSimulatorApp(appPath); err != nil {
			return Artifact{}, err
		}
		return Artifact{Path: appPath, Platform: PlatformIOS}, verifyDeclaredPlatform(declaredPlatform, PlatformIOS)
	default:
		return Artifact{}, fmt.Errorf("unsupported app_path %s: expected an .apk, a zipped simulator .app bundle, or a .app directory", filepath.Base(appPath))
	}
}

func (s DevicePreview) prepareAppDirectory(appPath, declaredPlatform string) (Artifact, error) {
	if !strings.EqualFold(filepath.Ext(appPath), ".app") {
		return Artifact{}, fmt.Errorf("app_path %s is a directory but not a .app bundle", filepath.Base(appPath))
	}

	if err := verifySimulatorPlist(filepath.Join(appPath, "Info.plist")); err != nil {
		return Artifact{}, err
	}

	tmpDir, err := os.MkdirTemp("", "device-preview")
	if err != nil {
		return Artifact{}, fmt.Errorf("create temp dir: %w", err)
	}

	zipPath := filepath.Join(tmpDir, filepath.Base(appPath)+".zip")
	s.logger.Printf("Zipping %s", filepath.Base(appPath))
	// isContentOnly=false keeps the .app directory itself inside the archive, which is the
	// layout RDE's installer looks for.
	if err := ziputil.ZipDir(appPath, zipPath, false); err != nil {
		return Artifact{}, fmt.Errorf("zip %s: %w", appPath, err)
	}

	return Artifact{Path: zipPath, Platform: PlatformIOS, TempDir: tmpDir}, verifyDeclaredPlatform(declaredPlatform, PlatformIOS)
}

func verifyDeclaredPlatform(declared, detected string) error {
	if declared == "" || declared == detected {
		return nil
	}

	return fmt.Errorf("the platform input says %q but the app is a %s build", declared, detected)
}

// verifyZippedSimulatorApp finds the .app bundle inside the archive and checks its Info.plist.
func verifyZippedSimulatorApp(zipPath string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", filepath.Base(zipPath), err)
	}
	defer func() {
		if err := reader.Close(); err != nil {
			return
		}
	}()

	for _, file := range reader.File {
		if !isAppInfoPlist(file.Name) {
			continue
		}

		contents, err := readZipEntry(file)
		if err != nil {
			return fmt.Errorf("read %s from %s: %w", file.Name, filepath.Base(zipPath), err)
		}

		return verifySimulatorPlistContents(contents, filepath.Base(zipPath))
	}

	return fmt.Errorf("%s does not contain a .app bundle — expected a zipped simulator app, for example $BITRISE_APP_DIR_PATH.zip", filepath.Base(zipPath))
}

// isAppInfoPlist matches the bundle's own Info.plist, not one belonging to a nested framework
// or plugin. `__MACOSX` entries are Finder's AppleDouble junk and never a real bundle.
func isAppInfoPlist(name string) bool {
	if strings.HasPrefix(name, "__MACOSX/") || strings.Contains(name, "/__MACOSX/") {
		return false
	}

	dir, file := path.Split(filepath.ToSlash(name))
	if file != "Info.plist" {
		return false
	}

	return strings.HasSuffix(strings.TrimSuffix(dir, "/"), ".app")
}

func readZipEntry(file *zip.File) ([]byte, error) {
	readCloser, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := readCloser.Close(); err != nil {
			return
		}
	}()

	return io.ReadAll(readCloser)
}

func verifySimulatorPlist(plistPath string) error {
	contents, err := os.ReadFile(plistPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", plistPath, err)
	}

	return verifySimulatorPlistContents(contents, filepath.Base(filepath.Dir(plistPath)))
}

type appInfoPlist struct {
	SupportedPlatforms []string `plist:"CFBundleSupportedPlatforms"`
	PlatformName       string   `plist:"DTPlatformName"`
}

// verifySimulatorPlistContents rejects device builds. `xcrun simctl` cannot install a binary
// built for iphoneos, so a preview link for one would resolve to a failed install.
func verifySimulatorPlistContents(contents []byte, name string) error {
	var info appInfoPlist
	if _, err := plist.Unmarshal(contents, &info); err != nil {
		return fmt.Errorf("parse Info.plist of %s: %w", name, err)
	}

	if strings.EqualFold(info.PlatformName, "iphonesimulator") {
		return nil
	}
	for _, platform := range info.SupportedPlatforms {
		if strings.EqualFold(platform, "iPhoneSimulator") {
			return nil
		}
	}

	return fmt.Errorf("%s is a device build (%s), which a Simulator cannot run — use a simulator build, for example $BITRISE_APP_DIR_PATH.zip from the Xcode build for simulator Step",
		name, describePlatform(info))
}

func describePlatform(info appInfoPlist) string {
	if info.PlatformName != "" {
		return info.PlatformName
	}
	if len(info.SupportedPlatforms) > 0 {
		return strings.Join(info.SupportedPlatforms, ", ")
	}

	return "unknown platform"
}

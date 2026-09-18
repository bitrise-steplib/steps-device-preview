package step

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/bitrise-io/go-utils/v2/log"
	"github.com/stretchr/testify/require"
)

const simulatorPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key><string>io.bitrise.Fruta</string>
	<key>CFBundleSupportedPlatforms</key><array><string>iPhoneSimulator</string></array>
	<key>DTPlatformName</key><string>iphonesimulator</string>
</dict>
</plist>`

const devicePlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key><string>io.bitrise.Fruta</string>
	<key>CFBundleSupportedPlatforms</key><array><string>iPhoneOS</string></array>
	<key>DTPlatformName</key><string>iphoneos</string>
</dict>
</plist>`

func TestPrepareArtifact(t *testing.T) {
	previewStep := New(log.NewLogger(), nil, nil)

	t.Run("an apk is an android build", func(t *testing.T) {
		path := writeFile(t, "app-release.apk", "not really an apk")

		artifact, err := previewStep.prepareArtifact(path, "")

		require.NoError(t, err)
		require.Equal(t, PlatformAndroid, artifact.Platform)
		require.Equal(t, path, artifact.Path)
	})

	t.Run("a zipped simulator app is an ios build", func(t *testing.T) {
		path := writeAppZip(t, "Fruta iOS.app.zip", "Fruta iOS.app/Info.plist", simulatorPlist)

		artifact, err := previewStep.prepareArtifact(path, "")

		require.NoError(t, err)
		require.Equal(t, PlatformIOS, artifact.Platform)
	})

	t.Run("a zipped device app is rejected", func(t *testing.T) {
		path := writeAppZip(t, "Fruta iOS.app.zip", "Fruta iOS.app/Info.plist", devicePlist)

		_, err := previewStep.prepareArtifact(path, "")

		require.ErrorContains(t, err, "device build")
	})

	t.Run("an aab is rejected", func(t *testing.T) {
		path := writeFile(t, "app-release.aab", "not really an aab")

		_, err := previewStep.prepareArtifact(path, "")

		require.ErrorContains(t, err, "App Bundle")
	})

	t.Run("an ipa is rejected", func(t *testing.T) {
		path := writeFile(t, "Fruta.ipa", "not really an ipa")

		_, err := previewStep.prepareArtifact(path, "")

		require.ErrorContains(t, err, "device build")
	})

	t.Run("a zip without an app bundle is rejected", func(t *testing.T) {
		path := writeAppZip(t, "logs.zip", "logs/build.log", "nothing to see here")

		_, err := previewStep.prepareArtifact(path, "")

		require.ErrorContains(t, err, "does not contain a .app bundle")
	})

	t.Run("an unknown extension is rejected", func(t *testing.T) {
		path := writeFile(t, "notes.txt", "hello")

		_, err := previewStep.prepareArtifact(path, "")

		require.ErrorContains(t, err, "unsupported app_path")
	})

	t.Run("a declared platform that contradicts the file is rejected", func(t *testing.T) {
		path := writeFile(t, "app-release.apk", "not really an apk")

		_, err := previewStep.prepareArtifact(path, PlatformIOS)

		require.ErrorContains(t, err, "the platform input says")
	})

	t.Run("a matching declared platform is accepted", func(t *testing.T) {
		path := writeFile(t, "app-release.apk", "not really an apk")

		artifact, err := previewStep.prepareArtifact(path, PlatformAndroid)

		require.NoError(t, err)
		require.Equal(t, PlatformAndroid, artifact.Platform)
	})

	t.Run("a .app directory is zipped", func(t *testing.T) {
		dir := t.TempDir()
		appDir := filepath.Join(dir, "Fruta iOS.app")
		require.NoError(t, os.MkdirAll(appDir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(appDir, "Info.plist"), []byte(simulatorPlist), 0o600))

		artifact, err := previewStep.prepareArtifact(appDir, "")

		require.NoError(t, err)
		require.Equal(t, PlatformIOS, artifact.Platform)
		require.Equal(t, "Fruta iOS.app.zip", filepath.Base(artifact.Path))
		require.FileExists(t, artifact.Path)
		// The zip is the Step's own, so Run has to know what to clean up.
		require.NotEmpty(t, artifact.TempDir)
		require.Equal(t, artifact.TempDir, filepath.Dir(artifact.Path))
	})

	t.Run("a .app directory declared as android is rejected without leaving a zip behind", func(t *testing.T) {
		scratch := t.TempDir()
		t.Setenv("TMPDIR", scratch)
		appDir := filepath.Join(t.TempDir(), "Fruta iOS.app")
		require.NoError(t, os.MkdirAll(appDir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(appDir, "Info.plist"), []byte(simulatorPlist), 0o600))

		_, err := previewStep.prepareArtifact(appDir, PlatformAndroid)

		require.ErrorContains(t, err, `the platform input says "android" but the app is a ios build`)
		leftovers, err := os.ReadDir(scratch)
		require.NoError(t, err)
		require.Empty(t, leftovers)
	})
}

func TestIsAppInfoPlist(t *testing.T) {
	require.True(t, isAppInfoPlist("Fruta iOS.app/Info.plist"))
	require.True(t, isAppInfoPlist("Payload/Fruta.app/Info.plist"))
	// Finder's "Compress" adds an AppleDouble shadow tree that mirrors real paths.
	require.False(t, isAppInfoPlist("__MACOSX/Fruta iOS.app/Info.plist"))
	// A framework's own Info.plist is not the bundle's.
	require.False(t, isAppInfoPlist("Fruta iOS.app/Frameworks/Kit.framework/Info.plist"))
	require.False(t, isAppInfoPlist("Fruta iOS.app/embedded.mobileprovision"))
}

func writeFile(t *testing.T, name, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))

	return path
}

func writeAppZip(t *testing.T, zipName, entryName, entryContents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), zipName)
	file, err := os.Create(path)
	require.NoError(t, err)

	writer := zip.NewWriter(file)
	entry, err := writer.Create(entryName)
	require.NoError(t, err)
	_, err = fmt.Fprint(entry, entryContents)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, file.Close())

	return path
}

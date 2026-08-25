# Device Preview

[![Step changelog](https://shields.io/github/v/release/bitrise-io/steps-device-preview?include_prereleases&label=changelog&color=blueviolet)](https://github.com/bitrise-io/steps-device-preview/releases)

Run this build on an iOS Simulator or Android Emulator straight from your pull request.

<details>
<summary>Description</summary>

Creates a shareable Device Preview link for an app artifact from this build. Opening the link boots a
fresh iOS Simulator or Android Emulator in the browser, installs the app and launches it — nothing to
download, and no local Xcode or Android SDK needed.

The link is exported as `$BITRISE_DEVICE_PREVIEW_URL`, so you can use it anywhere later in the Workflow:
post it to Slack, attach it to a GitHub check, drop it into release notes, or feed it to your own
notification tooling. Set `post_pr_comment` to `true` and the Step also posts it as a pull request
comment for you.

### Supported artifacts

- **iOS** — a zipped simulator `.app` bundle, for example the `$BITRISE_APP_DIR_PATH.zip` produced by the
  **Xcode build for simulator** Step. A Simulator cannot run a device build, so `.ipa` files are rejected.
- **Android** — an `.apk`. Android App Bundles (`.aab`) cannot be installed directly and are rejected.

If the file was already uploaded by an earlier **Deploy to Bitrise.io** Step, this Step reuses that
artifact instead of uploading it again.

### Good to know

- The Step fails the build if it cannot create a link. Set `is_skippable: true` on the Step in your
  Workflow if you would rather a preview problem did not stop the build.
- Links expire after 24 hours by default, and 72 hours is the maximum.
- Each open of a link starts its own session, which shuts down automatically once nobody is watching it.
- Device Preview has to be enabled for your Workspace.
</details>

## 🧩 Get started

Add this step directly to your workflow in the [Bitrise Workflow Editor](https://docs.bitrise.io/en/bitrise-ci/workflows-and-pipelines/steps/adding-steps-to-a-workflow.html).

You can also run this step directly with [Bitrise CLI](https://github.com/bitrise-io/bitrise).

## ⚙️ Configuration

<details>
<summary>Inputs</summary>

| Key | Description | Flags | Default |
| --- | --- | --- | --- |
| `app_path` | Path to the app to open on a device. It can be:  - a zipped iOS simulator app bundle, for example `$BITRISE_APP_DIR_PATH.zip` from the   **Xcode build for simulator** Step - an iOS simulator `.app` directory, which the Step zips for you - an Android `.apk`  Device builds (`.ipa`) and Android App Bundles (`.aab`) cannot be installed on a Simulator or Emulator, so the Step rejects them. | required | `$BITRISE_APP_DIR_PATH.zip` |
| `platform` | Leave empty to detect the platform from the app itself, which is what you usually want.  Set it to double-check the app you are passing in: the Step fails if the file turns out to be for the other platform. |  |  |
| `device_model` | Device to boot, for example `iPhone 15` on iOS or `pixel_7` on Android. Empty uses the platform default.  The value is not validated when the link is created, so a name that does not exist only surfaces when someone opens the link. |  |  |
| `os_version` | OS version to boot, for example `17.5`. Empty uses the platform default.  Only applies to iOS: on Android the OS version comes from the system image, so set `system_image` instead. |  |  |
| `stack` | Bitrise stack the preview sessions run on, for example `linux-docker-android-22.04`. Empty uses the default for the platform, which is what you usually want.  The value is validated when the link is created: a stack that does not exist, cannot be provisioned, or does not match the app's platform fails the Step with the backend's message. The available stacks can be listed via the Bitrise API using a Workspace API token (`GET /v1/workspaces/{workspace}/stacks`).  There is deliberately no cluster input — the cluster is derived from the stack and machine type combination, the same way session creation derives it. |  |  |
| `machine_type` | Machine type the preview sessions run on, for example `g2.linux.x-large`. Empty uses the default for the platform.  The value is validated when the link is created: a machine type that does not exist or is too small to run a preview device (an Android emulator needs at least 4 CPUs and 8 GB RAM) fails the Step with the backend's message. The available machine types can be listed via the Bitrise API using a Workspace API token (`GET /v1/workspaces/{workspace}/machine-types`). |  |  |
| `system_image` | Android only: the sdkmanager system image package the emulator boots, for example `system-images;android-34;google_apis;x86_64`. Empty uses the platform default.  Only images pre-installed on the stack can boot — they are never downloaded when the device starts. Asking for an image the stack does not have falls back to the closest installed one, with a notice shown to the viewer. |  |  |
| `emulator_ram_mb` | Android only: RAM given to the emulator, in MB. Empty sizes it to the host machine (a quarter of the host's RAM, clamped), which is what you usually want.  When set it must be at least 1024, and it is checked against the machine type the link's sessions run on when the link is created — asking for more RAM than the machine has fails the Step. |  |  |
| `emulator_cores` | Android only: CPU cores given to the emulator. Empty sizes it to the host machine (half of the host's cores, clamped), which is what you usually want.  Checked against the machine type the link's sessions run on when the link is created — asking for more cores than the machine has fails the Step. |  |  |
| `emulator_cold_boot` | Android only: boot the emulator cold (`-no-snapshot-load`) on every open of the link, and never save a quickboot snapshot. Every open then exercises a full first boot, which is slower but closer to what a fresh device does. | required | `false` |
| `link_ttl_hours` | Link lifetime in hours. Empty uses the default of 24 hours, and 72 hours is the maximum.  Short lifetimes are the main protection against a leaked link being used, so prefer the shortest one that still fits how your team reviews. |  |  |
| `auto_terminate_minutes` | How long a device session spawned by the link stays up after its last viewer disconnects, in minutes. Empty uses the default of 60 minutes.  When set it must be at least 10 minutes — a shorter window would shut sessions down before the device finishes booting — and at most the maximum session lifetime, which bounds a session's total runtime no matter what: auto-terminate can be tuned but never turned off. |  |  |
| `post_pr_comment` | Post the link as a comment on the pull request this build belongs to.  Off by default while Device Preview is being trialled, so switching the Step on somewhere busy cannot start commenting on everyone's pull requests. Ignored when the build is not a pull request build. The link is always exported as `$BITRISE_DEVICE_PREVIEW_URL` either way, so you can share it however you like. | required | `false` |
| `permanent_download_url_map` | Used to find the app among the artifacts a previous **Deploy to Bitrise.io** Step already uploaded, so the same file is not uploaded twice. When the app is not found here, the Step uploads it itself. |  | `$BITRISE_PERMANENT_DOWNLOAD_URL_MAP` |
| `verbose` | Enable verbose logging. | required | `false` |
| `build_url` | Unique build URL of this build on Bitrise.io. Set automatically. | required | `$BITRISE_BUILD_URL` |
| `build_api_token` | The build's API token for this build on Bitrise.io. Set automatically. | required, sensitive | `$BITRISE_BUILD_API_TOKEN` |
</details>

<details>
<summary>Outputs</summary>

| Environment Variable | Description |
| --- | --- |
| `BITRISE_DEVICE_PREVIEW_URL` | The shareable link that opens this build on a device. |
| `BITRISE_DEVICE_PREVIEW_EXPIRES_AT` | When the Device Preview link stops working, as an RFC 3339 timestamp. |
</details>

## 🙋 Contributing

We welcome [pull requests](https://github.com/bitrise-io/steps-device-preview/pulls) and [issues](https://github.com/bitrise-io/steps-device-preview/issues) against this repository.

For pull requests, work on your changes in a forked repository and use the Bitrise CLI to [run step tests locally](https://docs.bitrise.io/en/bitrise-ci/bitrise-cli/running-your-first-local-build-with-the-cli.html).

Learn more about developing steps:

- [Create your own step](https://docs.bitrise.io/en/bitrise-ci/workflows-and-pipelines/developing-your-own-bitrise-step/developing-a-new-step.html)

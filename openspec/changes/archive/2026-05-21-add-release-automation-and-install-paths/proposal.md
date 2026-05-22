## Why

DockBridge now has a stronger onboarding story, but it still lacks a complete
distribution path: there is no automated release build for GitHub, the README
does not yet present a polished quick-start flow, and the onboarding script
does not install the `dockerbridge` command itself. That leaves users with an
incomplete "first success" path even when Docker, SSH, and Mutagen are ready.

## What Changes

- Add GitHub Actions workflows that build DockBridge release binaries and
  publish them to GitHub Releases for `github.com/monlor/dockbridge`.
- Update the README quick-start and installation sections to recommend the safe
  one-command onboarding path first, followed by a clear manual installation
  path.
- Extend the onboarding script so it can install the `dockerbridge` binary
  itself and configure a convenient shell alias or command entrypoint for the
  user.
- Add verification and documentation for the binary distribution and local
  command-install flow.

## Capabilities

### New Capabilities
- `dockbridge-release-distribution`: Automated CI release builds and GitHub
  release publishing for DockBridge binaries.
- `dockbridge-installation-experience`: Local installation of the
  `dockerbridge` command, onboarding-managed shell alias setup, and
  documentation for the recommended quick-start order.

### Modified Capabilities
- None.

## Impact

- Affected code: `.github/workflows/**`, `scripts/onboard-dockbridge.sh`,
  `scripts/onboard-dockbridge.md`, and [README.md](/Users/monlor/Workspace/DockBridge/README.md)
- Affected systems: GitHub Actions, GitHub Releases, local shell profile or
  alias guidance, local binary install locations
- Verification surface: release artifact naming, cross-platform Go builds,
  onboarding install flow, alias/idempotency behavior, and README quick-start
  accuracy

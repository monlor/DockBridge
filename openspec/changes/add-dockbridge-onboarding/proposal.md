## Why

DockBridge depends on a working Docker CLI, SSH reachability, Mutagen, and a
usable Docker context before a user can do anything productive. The repository
already has an onboarding script prototype, but it is not yet a reliable
out-of-box path and currently contains shell-level issues that block successful
execution, so new users still need too much manual setup knowledge.

## What Changes

- Add a supported onboarding script as the primary first-run entry point for
  DockBridge setup.
- Detect the local OS and CPU architecture, prioritize macOS behavior, and
  install or validate the Docker CLI plus a matching Mutagen binary.
- Collect SSH connection details from the user, optionally create or update an
  `~/.ssh/config` alias, and validate the remote target before context setup.
- Create or refresh a Docker context for an `ssh://` remote target and offer to
  activate it immediately.
- Produce clear onboarding guidance, smoke checks, and troubleshooting output so
  a new user can reach a working DockBridge environment without reading the
  codebase first.

## Capabilities

### New Capabilities
- `dockbridge-onboarding`: First-run bootstrap flow for installing local
  dependencies, collecting SSH details, configuring a remote Docker context,
  and guiding a user through quick validation.

### Modified Capabilities
- None.

## Impact

- Affected code: `scripts/onboard-dockbridge.sh`, `scripts/onboard-dockbridge.md`,
  and [README.md](/Users/monlor/Workspace/DockBridge/README.md)
- Affected systems: local shell environment, `~/.ssh/config`, Docker CLI
  contexts, local Mutagen binary installation
- Verification surface: shell parsing, dry-run flows, dependency detection, SSH
  validation behavior, and onboarding smoke-check documentation

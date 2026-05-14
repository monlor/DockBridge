## 1. Script Hardening

- [x] 1.1 Repair the onboarding script shell syntax and command composition so
  it parses cleanly under `bash -n`.
- [x] 1.2 Implement macOS-first Docker CLI detection and installation behavior,
  while keeping best-effort Linux package-manager handling.
- [x] 1.3 Implement architecture-aware Mutagen download and installation with a
  safe fallback target directory and PATH guidance.

## 2. SSH And Context Flow

- [x] 2.1 Implement SSH host, port, user, and key collection with optional key
  generation and opt-in SSH alias management.
- [x] 2.2 Add guarded SSH alias overwrite behavior and a non-interactive SSH
  validation step with actionable failure output.
- [x] 2.3 Implement Docker context inspect, create or recreate, and optional
  activation behavior for `ssh://` remote targets.

## 3. Guidance And Verification

- [x] 3.1 Implement dry-run-safe onboarding output, final smoke checks, and
  optional summary-file generation.
- [x] 3.2 Update [README.md](/Users/monlor/Workspace/DockBridge/README.md) and
  `scripts/onboard-dockbridge.md` so the onboarding script is the documented
  quick-start path.
- [x] 3.3 Verify the completed flow with `bash -n`, `shellcheck`, and
  representative dry-run scenarios for the supported setup paths.

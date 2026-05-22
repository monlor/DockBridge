## 1. Release Automation

- [x] 1.1 Add GitHub Actions workflow files that build `dockerbridge` release
  binaries for the supported OS and architecture matrix.
- [x] 1.2 Add GitHub release publishing steps with predictable artifact names
  for `github.com/monlor/dockbridge`.
- [x] 1.3 Verify workflow syntax and artifact naming expectations against the
  documented install path.

## 2. Command Bootstrap

- [x] 2.1 Extend the onboarding script so it installs the `dockerbridge` binary
  itself into a usable local executable path.
- [x] 2.2 Add alias or shell-command setup flow with explicit confirmation and
  clear fallback guidance.
- [x] 2.3 Expand local smoke coverage to verify command installation and alias
  behavior without breaking the existing onboarding path.

## 3. Documentation And Verification

- [x] 3.1 Rewrite the README quick-start so the one-command onboarding path is
  the primary recommendation and manual installation is the secondary path.
- [x] 3.2 Update onboarding documentation with binary install and alias details
  that match the new script behavior.
- [x] 3.3 Validate the completed change with OpenSpec validation plus the
  relevant shell and documentation verification steps.

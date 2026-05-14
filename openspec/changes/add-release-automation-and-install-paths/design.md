## Context

DockBridge currently has source code, a local onboarding script, and a
lightweight README, but it does not yet have a complete distribution story.
There is no GitHub Actions workflow that builds tagged binaries and attaches
them to a GitHub release, so users cannot rely on a stable download path from
`github.com/monlor/dockbridge`. At the same time, the onboarding script focuses
on Docker, SSH, and Mutagen prerequisites, but not the `dockerbridge` command
itself, which leaves the quick-start story incomplete.

This change crosses release automation, documentation, and onboarding behavior.
It affects both repository automation and the first-run local install path, so
a design document is warranted.

## Goals / Non-Goals

**Goals:**
- Build and publish release binaries automatically with GitHub Actions.
- Expose a clear README installation flow that recommends the safe onboarding
  script first and manual installation second.
- Teach onboarding to install the `dockerbridge` binary locally and offer a
  shell alias or convenient invocation path.
- Keep the local installation flow idempotent and easy to verify.

**Non-Goals:**
- Adding package-manager distribution such as Homebrew tap, npm, or apt repos in
  this change.
- Redesigning DockBridge runtime behavior or CLI command semantics.
- Supporting every shell customization framework beyond a minimal alias-oriented
  install path and clear fallback guidance.

## Decisions

### Use GitHub Actions release workflows as the canonical distribution path

DockBridge should publish binaries from the repository itself so the README can
point users to a stable, project-owned installation source. GitHub Actions plus
GitHub Releases is the lightest path that still provides reproducible binaries,
artifact retention, and a familiar release experience.

Alternatives considered:
- Manual local release builds: rejected because they do not scale and are easy
  to forget or perform inconsistently.
- Third-party release tooling immediately: rejected because it adds extra setup
  and secret management before the basic release path exists.

### Build Go binaries for a focused cross-platform matrix

The release workflow should compile the `dockerbridge` binary for the primary
target operating systems and architectures that match the README story. The
matrix should prioritize macOS and Linux, with predictable artifact naming that
the onboarding or manual install docs can reference.

Alternatives considered:
- Single-platform release builds: rejected because the project is already
  framing onboarding as macOS-first with best-effort Linux support.
- Universal archives with custom packaging logic first: rejected because
  direct binaries are enough for a first release path.

### Keep onboarding as the safest recommended install surface

The README should recommend onboarding first because it verifies Docker, SSH,
Mutagen, the `dockerbridge` binary, and local command convenience in one flow.
Manual installation should remain available for users who want tighter control
or already manage prerequisites themselves.

Alternatives considered:
- Recommend manual install first: rejected because it fragments setup and loses
  the safety checks already built into onboarding.
- Hide manual install entirely: rejected because advanced users still need a
  deterministic fallback path.

### Install `dockerbridge` into a local executable path and offer alias setup

The onboarding script should install the binary into a writable local bin path
or another clearly reported target, then offer a minimal alias or command setup
step. The alias experience should be explicit and user-controlled rather than
silently mutating shell startup files without confirmation.

Alternatives considered:
- Modify shell rc files unconditionally: rejected because it is intrusive and
  hard to reason about across different shell setups.
- Avoid alias handling and only print the binary path: rejected because the user
  explicitly wants alias configuration as part of onboarding.

## Risks / Trade-offs

- [Release workflow drift] Artifact names or build matrix settings can drift
  from README examples or onboarding assumptions.
  → Mitigation: keep naming conventions explicit in workflow and test docs
  against the same naming pattern.

- [Alias persistence complexity] Different shells and user dotfile structures
  make persistent alias setup risky.
  → Mitigation: keep the default flow opt-in, target common shell files only
  with confirmation, and always print a manual fallback command.

- [Binary install path ambiguity] Local executable paths differ across macOS and
  Linux machines.
  → Mitigation: reuse detection-first path selection and log the chosen install
  target clearly.

- [Release permissions] GitHub Actions release publishing can fail if workflow
  permissions or tag triggers are misconfigured.
  → Mitigation: keep workflow permissions explicit and validate on a tag-driven
  dry run or repository test tag before announcing the path broadly.

## Migration Plan

1. Add release workflow files and confirm they produce predictable artifacts.
2. Extend onboarding to install the `dockerbridge` binary and configure alias
   guidance or persistence.
3. Rewrite README quick-start so onboarding is the first recommendation and
   manual installation is the second path.
4. Verify local onboarding behavior and repository workflow syntax before
   treating the release path as public-facing.

Rollback is straightforward: remove the new workflow files, revert README
priority order, and fall back to the current source-first onboarding flow.

## Open Questions

- Should alias setup write to shell rc files directly when confirmed, or should
  the script stop at printing the exact alias command to append?
- Should release assets include archives with checksums only, or both raw binary
  and compressed archive forms?

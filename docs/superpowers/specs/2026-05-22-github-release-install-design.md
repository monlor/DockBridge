# GitHub Release Build And Install Design

## Context

DockBridge already documents a release-based install path and the onboarding
script already downloads `releases/latest/download/dockerbridge_<platform>.tar.gz`
when no local repository binary is present. The repository also contains
release-related GitHub Actions workflows, but the current trigger model is
release-publication-driven instead of tag-push-driven, which does not match the
desired publishing flow.

The goal of this change is to make GitHub Releases the canonical distribution
channel for DockBridge binaries while keeping the install experience aligned
with the existing onboarding script and README contract.

## Goals

- Publish DockBridge release assets automatically when a version tag is pushed.
- Keep release artifact names stable and predictable for `install.sh` and
  README examples.
- Preserve `install.sh` behavior of installing from the latest GitHub Release
  when no local repository binary is available.
- Keep verification in CI so release naming and install assumptions do not
  silently drift.

## Non-Goals

- Adding explicit version selection to `install.sh`.
- Introducing package-manager distribution such as Homebrew, apt, npm, or a
  custom tap.
- Replacing GitHub Actions with GoReleaser or other release tooling in this
  iteration.

## Decisions

### Release trigger

Release publication will be triggered by `push` events on tags matching `v*`.
Pushing a version tag is the source of truth for release creation.

This replaces the current model where the workflow waits for a manually
published GitHub Release and then attaches assets afterward.

### Release implementation

The repository will keep a native GitHub Actions implementation rather than
adding GoReleaser. The release workflow will:

- build a four-target matrix: `darwin/linux x amd64/arm64`
- compile `./cmd/dockerbridge`
- package the binary into `.tar.gz` archives
- generate `.sha256` checksum files
- create or update the GitHub Release for the pushed tag
- enable GitHub-generated release notes

This is the lowest-friction path because the repository already has working
workflow structure and the current packaging logic is simple.

### Artifact contract

Published artifacts must keep these exact names:

- `dockerbridge_darwin_amd64.tar.gz`
- `dockerbridge_darwin_arm64.tar.gz`
- `dockerbridge_linux_amd64.tar.gz`
- `dockerbridge_linux_arm64.tar.gz`
- matching `.sha256` files for each archive

The artifact contract is shared by three surfaces:

- `release.yml` publishes these names
- `release-verify.yml` asserts these names exist
- `install.sh` and `README.md` reference the same naming pattern

### Install behavior

`install.sh` will keep its current behavior:

- if a local executable repository binary exists, install from that binary
- otherwise download from `releases/latest/download/...`

No new install flags will be added in this change. The default install path
remains latest-release based.

### README contract

The README will continue to present:

- `./install.sh` as the recommended path
- direct manual download from GitHub Releases as the secondary path

The documentation must stay aligned with the exact artifact names emitted by
the release workflow.

## Workflow Design

### `release.yml`

`release.yml` will be updated so that:

- trigger = `push.tags: v*`
- permissions keep `contents: write`
- each matrix leg builds one archive and one checksum
- release publication uses the pushed tag name
- release notes are generated with `generate_release_notes: true`
- uploaded files can be overwritten on rerun for the same tag

To avoid a broken `latest` release state, publication happens only from a
successful run. A failed matrix leg prevents the workflow from completing and
prevents the new release from becoming the completed published output for users.

### `release-verify.yml`

`release-verify.yml` remains the preflight guard on pull requests and `main`.
It will verify:

- the four expected archive names exist
- Go tests pass
- onboarding script smoke tests pass
- referenced script paths are real and valid
- README/manual install naming stays aligned with workflow output

## Risks And Mitigations

### Incomplete latest-release assets

Risk: users install from `latest`, so a partially published release would break
installation.

Mitigation: release creation happens from the tag-driven workflow only after the
workflow reaches its release-upload step. If any matrix leg fails, the workflow
fails and does not complete publication for that run.

### Naming drift

Risk: README, verification workflow, and `install.sh` can diverge on asset
names.

Mitigation: keep names hard-coded and tested in CI. The verification workflow
must fail if expected archive names disappear or if documentation/examples point
at different names.

### Script-path drift

Risk: the verification workflow may call a nonexistent onboarding script path.

Mitigation: correct workflow references to actual repository files and keep a
syntax check plus smoke test in CI.

## Implementation Plan

1. Update `.github/workflows/release.yml` to publish on `push.tags: v*` with
   generated notes.
2. Fix and strengthen `.github/workflows/release-verify.yml` so it validates
   the release artifact contract and real script paths.
3. Keep `install.sh` behavior unchanged unless verification finds a contract
   mismatch.
4. Adjust `README.md` only if needed to match the final verified artifact and
   workflow contract.
5. Run local verification:
   - `go test ./...`
   - `bash scripts/test-onboard-dockbridge.sh`
   - `bash -n install.sh`
   - targeted workflow/config sanity checks

## Acceptance Criteria

- Pushing a `v*` tag automatically creates or updates a GitHub Release with the
  four expected archives and four checksum files.
- GitHub-generated release notes are enabled.
- `install.sh` continues to install from `latest` release assets with the same
  filename convention.
- CI fails if artifact names, script paths, or install-doc assumptions drift.

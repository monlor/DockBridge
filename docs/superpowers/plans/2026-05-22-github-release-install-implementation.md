# GitHub Release Install Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Switch DockBridge release publishing to tag-push automation, keep the `latest` install contract stable, and verify the release/install naming contract in CI.

**Architecture:** Keep the existing GitHub Actions shape, but change the release trigger from release-publication to tag-push and make the verification workflow assert the same artifact contract consumed by `install.sh` and documented in `README.md`. Avoid changing installer behavior unless CI exposes a mismatch.

**Tech Stack:** GitHub Actions YAML, Bash, Go toolchain, existing repository smoke tests

---

### Task 1: Lock down the release/install contract in verification

**Files:**
- Modify: `.github/workflows/release-verify.yml`
- Test: `.github/workflows/release-verify.yml`
- Test: `install.sh`
- Test: `README.md`

- [ ] **Step 1: Write the failing verification checks**

Add a workflow step that fails when:
- `install.sh` no longer references `releases/latest/download/dockerbridge_%s.tar.gz`
- `README.md` no longer references all four archive names
- the workflow still syntax-checks a nonexistent `scripts/onboard-dockbridge.sh`

Target shell snippet:

```bash
grep -Fq 'releases/latest/download/dockerbridge_%s.tar.gz' install.sh
grep -Fq 'dockerbridge_darwin_amd64.tar.gz' README.md
grep -Fq 'dockerbridge_darwin_arm64.tar.gz' README.md
grep -Fq 'dockerbridge_linux_amd64.tar.gz' README.md
grep -Fq 'dockerbridge_linux_arm64.tar.gz' README.md
test -f install.sh
```

- [ ] **Step 2: Run the relevant checks to see current failure**

Run:

```bash
grep -F 'scripts/onboard-dockbridge.sh' .github/workflows/release-verify.yml
test -f scripts/onboard-dockbridge.sh
```

Expected: the grep finds the stale path, and `test -f` fails because that file does not exist.

- [ ] **Step 3: Implement minimal verification fixes**

Update `.github/workflows/release-verify.yml` so the validation job:
- syntax-checks `install.sh`
- runs `bash scripts/test-onboard-dockbridge.sh`
- runs the `grep` assertions above against `install.sh` and `README.md`

Core shell block:

```bash
bash -n install.sh
bash scripts/test-onboard-dockbridge.sh
grep -Fq 'releases/latest/download/dockerbridge_%s.tar.gz' install.sh
grep -Fq 'dockerbridge_darwin_amd64.tar.gz' README.md
grep -Fq 'dockerbridge_darwin_arm64.tar.gz' README.md
grep -Fq 'dockerbridge_linux_amd64.tar.gz' README.md
grep -Fq 'dockerbridge_linux_arm64.tar.gz' README.md
go test ./...
```

- [ ] **Step 4: Re-run the targeted checks**

Run:

```bash
grep -F 'scripts/onboard-dockbridge.sh' .github/workflows/release-verify.yml
bash -n .github/workflows/release-verify.yml
```

Expected: no stale script-path reference remains. YAML syntax command may need replacement if plain `bash -n` is not suitable; if so, use a repo-available YAML parse sanity check instead.

### Task 2: Switch release publishing to tag-push automation

**Files:**
- Modify: `.github/workflows/release.yml`
- Test: `.github/workflows/release.yml`

- [ ] **Step 1: Write the failing trigger expectation**

Define the required release trigger contract:
- workflow must trigger on `push.tags: v*`
- version source must come from the pushed ref tag, not `github.event.release.tag_name`

Target shell snippet:

```bash
grep -Fq 'push:' .github/workflows/release.yml
grep -Fq 'tags:' .github/workflows/release.yml
grep -Fq -- '- v*' .github/workflows/release.yml
! grep -Fq 'github.event.release.tag_name' .github/workflows/release.yml
```

- [ ] **Step 2: Run the trigger expectation against current workflow**

Run:

```bash
grep -Fn 'github.event.release.tag_name' .github/workflows/release.yml
grep -Fn 'release:' .github/workflows/release.yml
```

Expected: current workflow still uses `release.published` and `github.event.release.tag_name`.

- [ ] **Step 3: Implement minimal workflow changes**

Update `.github/workflows/release.yml` to:
- trigger on tag pushes matching `v*`
- derive `VERSION` from `${GITHUB_REF_NAME}`
- set `generate_release_notes: true`
- keep the existing matrix, artifact names, checksum files, and overwrite behavior

Core YAML fragment:

```yaml
on:
  push:
    tags:
      - 'v*'
```

Core build env:

```yaml
env:
  VERSION: ${{ github.ref_name }}
```

Core release settings:

```yaml
generate_release_notes: true
```

- [ ] **Step 4: Re-run the trigger expectation**

Run:

```bash
grep -Fq 'push:' .github/workflows/release.yml
grep -Fq 'tags:' .github/workflows/release.yml
grep -Fq 'generate_release_notes: true' .github/workflows/release.yml
! grep -Fq 'github.event.release.tag_name' .github/workflows/release.yml
```

Expected: all checks pass.

### Task 3: Verify the repo end to end

**Files:**
- Test: `.github/workflows/release.yml`
- Test: `.github/workflows/release-verify.yml`
- Test: `install.sh`
- Test: `scripts/test-onboard-dockbridge.sh`
- Test: `README.md`

- [ ] **Step 1: Run repository verification commands**

Run:

```bash
go test ./...
bash scripts/test-onboard-dockbridge.sh
bash -n install.sh
```

Expected: all exit successfully.

- [ ] **Step 2: Run targeted release contract checks**

Run:

```bash
grep -Fq 'releases/latest/download/dockerbridge_%s.tar.gz' install.sh
grep -Fq 'dockerbridge_darwin_amd64.tar.gz' README.md
grep -Fq 'dockerbridge_darwin_arm64.tar.gz' README.md
grep -Fq 'dockerbridge_linux_amd64.tar.gz' README.md
grep -Fq 'dockerbridge_linux_arm64.tar.gz' README.md
```

Expected: all checks pass.

- [ ] **Step 3: Review changed files before final report**

Run:

```bash
git diff -- .github/workflows/release.yml .github/workflows/release-verify.yml README.md install.sh
git status --short .github/workflows/release.yml .github/workflows/release-verify.yml README.md install.sh docs/superpowers/specs/2026-05-22-github-release-install-design.md docs/superpowers/plans/2026-05-22-github-release-install-implementation.md
```

Expected: only intended files are reported, with no accidental installer contract drift.

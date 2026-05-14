## Context

DockBridge is a remote-Docker helper that depends on several local prerequisites
before a user can run real workloads: Docker CLI, SSH access to the remote
host, Mutagen, and a Docker context that points at the remote daemon over SSH.
This repository already contains a prototype onboarding script and matching
README sections, which confirms the intended user flow, but the current script
is not yet robust enough to be treated as the supported first-run path. The
prototype also needs stronger macOS-first behavior because that is the primary
target environment.

The change crosses several setup surfaces at once: local package detection and
installation, `~/.ssh/config` management, Docker context creation, and
user-facing setup guidance. That makes a design artifact useful before
implementation.

## Goals / Non-Goals

**Goals:**
- Provide a single supported onboarding entry point for first-run DockBridge
  setup.
- Make the flow work well on macOS first while preserving best-effort Linux
  support.
- Gather the minimum SSH details needed to connect to a remote Docker host and
  turn them into a reusable Docker context.
- Install or validate architecture-matched Mutagen and a usable Docker CLI.
- Give the user enough verification output to confirm they are ready to use
  DockBridge immediately after setup.

**Non-Goals:**
- Replacing the onboarding shell script with a Go subcommand in this change.
- Managing remote server provisioning beyond SSH and Docker context reachability.
- Automating shell profile edits beyond printing PATH guidance when needed.
- Changing DockBridge runtime behavior, sync internals, or session lifecycle.

## Decisions

### Keep onboarding as a standalone shell script

The primary user need is "clone repo and get to first success quickly." A shell
script remains the lowest-friction entry point because it can install tools,
prompt for SSH data, and integrate cleanly with README examples before the
DockBridge binary itself is trusted or configured.

Alternatives considered:
- Add a Go onboarding subcommand: rejected for this change because it adds
  binary release and bootstrap coupling before the local prerequisites exist.
- Provide docs-only setup steps: rejected because the user explicitly wants an
  automatic onboarding path.

### Make macOS the first-class install path

The script should branch early on OS and architecture. On macOS it should
prefer Homebrew for Docker CLI installation and select the correct Mutagen
artifact for Apple Silicon or Intel. Linux remains best-effort with package
manager detection and architecture-matched Mutagen download.

Alternatives considered:
- Treat all platforms uniformly with generic curl installers: rejected because
  it reduces reliability and user trust on macOS, which is the main target.
- Limit the change to macOS only: rejected because the existing script already
  signals Linux intent and the user asked for architecture matching, not a
  single-platform hard stop.

### Use guarded, prompt-driven SSH configuration

The onboarding flow should collect host, port, user, and key path, then offer
to create or update a named `~/.ssh/config` alias. Local SSH config changes
must remain opt-in, and any overwrite of an existing alias must be explicitly
confirmed. If the configured private key is missing, the script may offer to
generate one, but it should not assume remote key distribution is complete.

Alternatives considered:
- Always write an SSH alias: rejected because some users already manage SSH
  config externally.
- Avoid touching `~/.ssh/config`: rejected because a stable alias significantly
  improves repeatability and makes Docker context creation clearer.

### Represent the remote daemon with a named Docker context

The supported outcome is a named Docker context backed by `ssh://...`. The
script should inspect whether the target context already exists, offer to
recreate it when necessary, and optionally switch the active context after
creation. The Docker host value should prefer an SSH alias when one is created,
and otherwise fall back to the explicit `user@host:port` form.

Alternatives considered:
- Export `DOCKER_HOST` only: rejected because a named context is more durable,
  easier to inspect, and better matches user expectations for repeated use.
- Always recreate the context: rejected because it is unnecessarily destructive
  for users who already have a working context.

### Treat verification output as part of the product

The onboarding script is not complete when it has merely created files or run
install commands. It should also produce a concise summary, support `--dry-run`
for safe rehearsal, and print a final smoke checklist that confirms the user
can validate the environment without reading source code.

Alternatives considered:
- Keep verification only in README prose: rejected because users need immediate
  next steps at the end of the script.

## Risks / Trade-offs

- [Local environment variability] Different Homebrew paths, missing package
  managers, or restricted write permissions can break installs.
  → Mitigation: prefer detection-first logic, clear fallback messages, and a
  home-directory install path when system locations are unavailable.

- [SSH config safety] Updating `~/.ssh/config` can conflict with user-managed
  aliases or formatting.
  → Mitigation: make alias creation opt-in, scope rewrites to one named host
  block, and require confirmation before overwrite.

- [Network-dependent setup] Docker or Mutagen downloads can fail due to network
  issues or release URL changes.
  → Mitigation: surface the exact failing step, avoid partial success claims,
  and document manual recovery paths.

- [Cross-platform shell bugs] Adding Linux package-manager support can easily
  introduce parse or quoting regressions, especially in arrays and command
  composition.
  → Mitigation: require `bash -n`, `shellcheck`, and dry-run coverage as part
  of the verification plan.

## Migration Plan

1. Normalize the existing onboarding script into a valid, shippable baseline.
2. Update the README and script-local documentation so the onboarding script is
   the documented quick-start path.
3. Verify macOS behavior first, then run best-effort Linux dry-run coverage.
4. Keep existing DockBridge runtime entry points unchanged so rollout is
   documentation-first and low-risk.

Rollback is simple: remove the quick-start references to the onboarding script
and keep DockBridge setup manual if the automated flow proves unreliable.

## Open Questions

- Should automatic Docker installation on macOS target the standalone CLI
  formula, Docker Desktop, or support both with different prompts?
- Do we want an option to persist the onboarding summary in a default path for
  support/debugging, or should file output remain opt-in only?

# dockbridge-onboarding Specification

## Purpose
TBD - created by archiving change add-dockbridge-onboarding. Update Purpose after archive.
## Requirements
### Requirement: Onboarding script provisions local prerequisites
The system SHALL provide a supported onboarding script that detects the local
operating system and CPU architecture, prioritizes macOS behavior, and ensures
the user has access to a Docker CLI plus a matching Mutagen binary before
continuing with remote context setup.

#### Scenario: macOS user is missing Docker CLI
- **WHEN** the onboarding script runs on macOS and the `docker` command is not
  available
- **THEN** the script SHALL offer an automatic installation path that is
  appropriate for macOS
- **AND** the script SHALL explain the manual fallback if automatic
  installation cannot proceed

#### Scenario: matching Mutagen binary is required
- **WHEN** the onboarding script determines that Mutagen is not available
- **THEN** the script SHALL resolve the correct release artifact for the
  current supported OS and architecture
- **AND** the script SHALL install or stage a usable `mutagen` binary in a
  location it reports back to the user

### Requirement: Onboarding script captures and validates SSH access details
The system SHALL collect the SSH host, port, user, and key path needed for a
remote Docker connection, and it SHALL let the user opt into SSH config updates
and reachability validation before creating a Docker context.

#### Scenario: user wants a reusable SSH alias
- **WHEN** the user opts into SSH config management during onboarding
- **THEN** the script SHALL offer to create or update a named `~/.ssh/config`
  host entry using the collected SSH details
- **AND** the script SHALL require confirmation before replacing an existing
  alias block

#### Scenario: SSH validation is enabled
- **WHEN** the user does not skip SSH validation
- **THEN** the script SHALL run a non-interactive SSH reachability check
- **AND** the script SHALL report success or actionable failure information
  before Docker context creation continues

### Requirement: Onboarding script creates a reusable Docker context
The system SHALL translate the collected SSH target into a named Docker context
that points to an `ssh://` remote host and allows the user to activate it
immediately.

#### Scenario: target context does not exist yet
- **WHEN** the requested Docker context name is not present locally
- **THEN** the script SHALL create a new Docker context using the resolved
  remote SSH target
- **AND** the script SHALL offer to switch the active Docker context to the new
  context

#### Scenario: target context already exists
- **WHEN** the requested Docker context name already exists locally
- **THEN** the script SHALL tell the user it already exists
- **AND** the script SHALL require confirmation before recreating or replacing
  that context

### Requirement: Onboarding script provides guided verification output
The system SHALL support safe rehearsal and clear post-setup guidance so users
can confirm onboarding results without inspecting the implementation.

#### Scenario: user runs onboarding in dry-run mode
- **WHEN** the script is executed with `--dry-run`
- **THEN** the script SHALL print the actions it would take without mutating
  local tools, SSH config, or Docker contexts

#### Scenario: onboarding completes or reaches handoff
- **WHEN** the script finishes its main flow
- **THEN** the script SHALL print a concise smoke checklist covering Docker
  context inspection, context activation, and Mutagen verification
- **AND** the script SHALL include any follow-up guidance needed when PATH or
  SSH issues prevent immediate success


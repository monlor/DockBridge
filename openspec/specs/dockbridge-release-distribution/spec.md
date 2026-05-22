# dockbridge-release-distribution Specification

## Purpose
TBD - created by archiving change add-release-automation-and-install-paths. Update Purpose after archive.
## Requirements
### Requirement: Release workflow builds DockBridge binaries automatically
The system SHALL provide a GitHub Actions workflow that builds the `dockerbridge`
binary for the supported release matrix whenever a release-triggering event is
published.

#### Scenario: tagged release is created
- **WHEN** a repository tag or release event matches the release workflow
- **THEN** GitHub Actions SHALL build `dockerbridge` binaries for the supported
  OS and architecture matrix
- **AND** the produced artifacts SHALL use predictable names that identify the
  target platform

### Requirement: Release workflow publishes artifacts to GitHub Releases
The system SHALL attach the generated release artifacts to the corresponding
GitHub release for `github.com/monlor/dockbridge`.

#### Scenario: build matrix completes successfully
- **WHEN** all release build jobs finish without error
- **THEN** the workflow SHALL publish the built artifacts to the matching GitHub
  release
- **AND** the release output SHALL be usable by README installation guidance

### Requirement: Release documentation aligns with published assets
The system SHALL document the release install path in a way that matches the
actual artifact naming and download surface.

#### Scenario: user follows release-based installation docs
- **WHEN** a user reads the release installation section in the repository docs
- **THEN** the documented artifact source SHALL match the binaries published by
  the release workflow


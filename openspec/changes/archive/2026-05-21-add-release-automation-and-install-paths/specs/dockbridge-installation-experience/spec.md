## ADDED Requirements

### Requirement: Onboarding installs the dockerbridge command
The system SHALL let the onboarding flow install the `dockerbridge` command
itself into a usable local executable path in addition to provisioning Docker,
SSH, and Mutagen prerequisites.

#### Scenario: dockerbridge is not installed yet
- **WHEN** onboarding determines that the `dockerbridge` command is not
  available locally
- **THEN** the flow SHALL install or stage the `dockerbridge` binary in a local
  executable path
- **AND** the flow SHALL report where the command was installed

### Requirement: Onboarding offers shell alias convenience
The system SHALL offer an alias-oriented convenience step so the user can invoke
`dockerbridge` reliably after onboarding.

#### Scenario: user opts into alias setup
- **WHEN** onboarding offers shell alias or shell command setup and the user
  accepts
- **THEN** the flow SHALL either persist the alias in a confirmed shell startup
  file or print the exact command required for the user to add it
- **AND** the flow SHALL avoid changing shell startup files without confirmation

### Requirement: README recommends onboarding first
The system SHALL present the safe onboarding script as the primary quick-start
path and manual installation as the fallback path in repository documentation.

#### Scenario: user opens the quick-start section
- **WHEN** a user reads the README installation guidance
- **THEN** the first recommended path SHALL be the onboarding script
- **AND** the manual installation path SHALL appear as a separate follow-up
  option with clear prerequisites

### Requirement: Manual installation path remains available
The system SHALL document a manual installation flow for users who do not want
to rely on the onboarding script.

#### Scenario: user prefers manual installation
- **WHEN** a user chooses not to use the onboarding script
- **THEN** the documentation SHALL provide a manual path to obtain the
  `dockerbridge` binary and make it runnable locally

# DockBridge

DockBridge is a remote-Docker helper for running local-friendly commands against
SSH-backed remote Docker hosts.

## Quick Start (Onboarding)

The recommended first-run path is the install script:

```bash
./install.sh
```

The script is optimized for macOS first and includes best-effort Linux support.
It can:

1. install `docker` CLI when it is missing
2. install `mutagen`
3. install the `dockerbridge` command locally
4. offer a shell alias when the install directory is not already in `PATH`
5. reuse an existing SSH-backed Docker context or collect SSH target information
6. create and optionally activate a Docker context for the remote daemon

Use dry-run mode if you want to preview every command first:

```bash
./install.sh --dry-run
```

`--dry-run` prints the install, SSH, and Docker context actions it would take
without modifying local tools, `~/.ssh/config`, Docker contexts, or the
optional summary output file.

If `dockerbridge` lands in a directory that is not already in `PATH`, the
script prints an exact alias command or, with confirmation, writes one to your
shell startup file.

Common options:

- `--help`
- `--dry-run`
- `--skip-mutagen`
- `--skip-docker-check`
- `--skip-ssh-check`
- `--context-name NAME`
- `--output FILE`

Example:

```bash
./install.sh --context-name local-dockbridge --output /tmp/onboard.txt
```

During setup, you can either pick an existing Docker context whose Docker host
uses `ssh://` or create a new SSH-backed context. When you create a new
context, the server name becomes the default SSH alias and Docker context name
unless you override the context with `--context-name`.

## Manual Installation

If you prefer not to use the onboarding script, install a release asset from
GitHub Releases. The published asset names are predictable:

- `dockerbridge_darwin_amd64.tar.gz`
- `dockerbridge_darwin_arm64.tar.gz`
- `dockerbridge_linux_amd64.tar.gz`
- `dockerbridge_linux_arm64.tar.gz`
- `dockerbridge_<platform>.tar.gz.sha256`

Example for Apple Silicon macOS:

```bash
curl -fsSL -o /tmp/dockerbridge.tar.gz \
  https://github.com/monlor/dockbridge/releases/latest/download/dockerbridge_darwin_arm64.tar.gz
tar -xzf /tmp/dockerbridge.tar.gz -C /tmp
install -m 0755 /tmp/dockerbridge "$HOME/.local/bin/dockerbridge"
```

Example for Linux x86_64:

```bash
curl -fsSL -o /tmp/dockerbridge.tar.gz \
  https://github.com/monlor/dockbridge/releases/latest/download/dockerbridge_linux_amd64.tar.gz
tar -xzf /tmp/dockerbridge.tar.gz -C /tmp
install -m 0755 /tmp/dockerbridge "$HOME/.local/bin/dockerbridge"
```

If `~/.local/bin` is not already in `PATH`, either export it or add an alias:

```bash
alias dockerbridge="$HOME/.local/bin/dockerbridge"
```

## Troubleshooting

- Verify the installed command:
  `dockerbridge --version`
- If mutagen was installed into `~/.local/bin`, add it to PATH:
  `export PATH="$HOME/.local/bin:$PATH"`
- If SSH reachability fails, verify:
  - remote host reachable
  - key path is correct
  - port is open
  - user permissions are correct
- If context create fails, confirm SSH config / command values and retry with
  `--skip-ssh-check` if your environment intentionally blocks interactive checks.

## Script Verification

Use the built-in smoke test to validate the onboarding flow before shipping
changes:

```bash
bash scripts/test-onboard-dockbridge.sh
```

Release packaging is verified by the `Release Verify` GitHub Actions workflow.

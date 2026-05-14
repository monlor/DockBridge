# DockBridge Onboarding Script

`onboard-dockbridge.sh` helps new users complete a local bootstrap flow:

- install missing dependencies (`docker` CLI, `mutagen`)
- install the `dockerbridge` command locally
- offer a shell alias when the install directory is not already in `PATH`
- collect a server name that becomes the default SSH alias and Docker context
- collect SSH target info
- create and use a Docker context for remote SSH access
- print a quick validation checklist

## Run

```bash
./scripts/onboard-dockbridge.sh
```

Options:

- `--help` show usage
- `--dry-run` print intended commands only
- `--skip-mutagen` skip mutagen install/check
- `--skip-docker-check` skip docker installation/check
- `--skip-ssh-check` skip SSH connection check
- `--context-name NAME` use a custom Docker context name
- `--output FILE` write onboarding summary

## macOS first-run

On macOS, the script favors Homebrew to install the Docker CLI and then
installs the matching Mutagen binary for `darwin_amd64` or `darwin_arm64`.
For `dockerbridge`, the script installs a local repository binary when one is
already present in the checkout, otherwise it downloads the latest GitHub
release asset named `dockerbridge_darwin_amd64.tar.gz` or
`dockerbridge_darwin_arm64.tar.gz`. The install target prefers a writable
Homebrew or system bin directory before falling back to `~/.local/bin`.

```bash
./scripts/onboard-dockbridge.sh --dry-run
```

If Docker is not already installed, the script will print a short prompt and run
`brew install docker` when confirmed.

Dry-run mode prints the exact install, SSH, and Docker context commands it would
execute without changing local tools, `~/.ssh/config`, Docker contexts, or the
optional summary output file.

If the chosen install directory is not already in `PATH`, the script prints an
exact alias command and, in interactive mode, can append that alias to your
confirmed shell startup file.

## Linux first-run

On Linux, the script tries a best-effort install based on detected package
manager (`apt-get`, `dnf`, `yum`, `pacman`, or `zypper`) and then installs
architecture-matched Mutagen plus the matching `dockerbridge` release asset
(`dockerbridge_linux_amd64.tar.gz` or `dockerbridge_linux_arm64.tar.gz`) when a
local repository binary is not already available.

```bash
./scripts/onboard-dockbridge.sh --skip-docker-check
```

## Example smoke checklist

After onboarding completes, run these locally:

```bash
docker context ls
dockerbridge --version
docker context use dockbridge-remote
mutagen --version
docker run --rm hello-world
```

If mutagen was installed to `~/.local/bin`, ensure the directory is in `PATH`.

```bash
export PATH="$HOME/.local/bin:$PATH"
```

## Verification and smoke checks

You can validate the script before release:

```bash
shellcheck scripts/onboard-dockbridge.sh
bash -n scripts/onboard-dockbridge.sh
bash scripts/test-onboard-dockbridge.sh
```

Then run full smoke manually after dependencies are present:

```bash
./scripts/onboard-dockbridge.sh --context-name dockbridge-remote --output /tmp/dockbridge-onboarding.log
```

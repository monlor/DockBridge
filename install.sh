#!/usr/bin/env bash
set -euo pipefail
IFS=$'\n\t'

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$SCRIPT_DIR"
OS="$(uname -s)"
ARCH=""
MUTAGEN_PLATFORM=""
DOCKBRIDGE_PLATFORM=""
DRY_RUN=0
SKIP_MUTAGEN=0
SKIP_DOCKER_CHECK=0
SKIP_SSH_CHECK=0
OUTPUT_SUMMARY=""
CONTEXT_NAME="dockbridge-remote"
CONTEXT_NAME_EXPLICIT=0
USE_EXISTING_CONTEXT=0
SERVER_NAME=""
SSH_HOST=""
SSH_PORT=""
SSH_USER=""
SSH_KEY=""
SSH_ALIAS=""
DOCKER_HOST=""
DOCKBRIDGE_INSTALL_PATH=""
DOCKBRIDGE_INSTALL_SOURCE="not installed"
SHELL_ALIAS_STATUS="not needed"
DOCKER_COMMAND_ALIAS_ENABLED=0

log_info() {
  printf '[info] %s\n' "$*"
}

log_warn() {
  printf '[warn] %s\n' "$*" >&2
}

log_error() {
  printf '[error] %s\n' "$*" >&2
}

dry_run_cmd() {
  if [[ "$DRY_RUN" == "1" ]]; then
    printf '[dry-run]'
    while [[ $# -gt 0 ]]; do
      printf ' %s' "$1"
      shift
    done
    printf '\n'
    return 0
  fi

  "$@"
}

usage() {
  cat <<'USAGE'
Usage: install.sh [options]

Options:
  --help                 Show this help text.
  --dry-run              Print actions without applying changes.
  --skip-mutagen         Skip Mutagen check/install.
  --skip-docker-check    Skip Docker binary installation/check.
  --skip-ssh-check       Skip SSH reachability check.
  --context-name NAME    Docker context name to create (default: derived from server name).
  --output FILE          Write onboarding summary to FILE.

The script helps you:
  1) install docker CLI (best effort)
  2) install mutagen for your platform
  3) install the dockerbridge command locally
  4) collect SSH target details
  5) create a Docker context for ssh:// target
  6) print a quick post-setup checklist
USAGE
}

need_command() {
  local cmd="$1"
  command -v "$cmd" >/dev/null 2>&1
}

slugify() {
  local value="$1"
  local slug=""

  slug="$(printf '%s' "$value" | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9._-]/-/g; s/--*/-/g; s/^-//; s/-$//')"
  if [[ -z "$slug" ]]; then
    printf 'dockbridge-remote\n'
    return
  fi

  printf '%s\n' "$slug"
}

expand_path() {
  local path="$1"

  case "$path" in
    \~)
      printf '%s\n' "$HOME"
      ;;
    \~/*)
      printf '%s/%s\n' "$HOME" "${path#\~/}"
      ;;
    *)
      printf '%s\n' "$path"
      ;;
  esac
}

assert_dependencies() {
  local missing=0

  for cmd in curl tar awk sed find head mktemp cp chmod grep; do
    if ! need_command "$cmd"; then
      log_error "Missing required command: $cmd"
      missing=1
    fi
  done

  if [[ "$missing" -eq 1 ]]; then
    exit 1
  fi
}

ask() {
  local prompt="$1"
  local default="${2:-}"
  local answer=""

  if [[ -n "$default" ]]; then
    read -r -p "$prompt [$default]: " answer
    printf '%s\n' "${answer:-$default}"
    return
  fi

  read -r -p "$prompt: " answer
  printf '%s\n' "$answer"
}

path_contains_dir() {
  local dir="$1"
  [[ ":$PATH:" == *":$dir:"* ]]
}

shell_alias_is_interactive() {
  [[ "${DOCKBRIDGE_FORCE_INTERACTIVE:-0}" == "1" || -t 0 ]]
}

ask_yes_no() {
  local prompt="$1"
  local default="${2:-y}"
  local answer=""
  local normalized=""

  while true; do
    if [[ "$default" == "y" ]]; then
      read -r -p "$prompt [Y/n]: " answer
      answer="${answer:-y}"
    else
      read -r -p "$prompt [y/N]: " answer
      answer="${answer:-n}"
    fi

    normalized="$(printf '%s' "$answer" | tr '[:upper:]' '[:lower:]')"

    case "$normalized" in
      y|yes)
        printf '0\n'
        return
        ;;
      n|no)
        printf '1\n'
        return
        ;;
      *)
        log_warn "Please answer y or n."
        ;;
    esac
  done
}

ask_choice() {
  local prompt="$1"
  shift
  local options=("$@")
  local answer=""
  local index=1

  while true; do
    printf '%s\n' "$prompt" >&2
    for option in "${options[@]}"; do
      printf '  %s) %s\n' "$index" "$option" >&2
      index=$((index + 1))
    done

    read -r -p "Choose [1-${#options[@]}]: " answer
    if [[ "$answer" =~ ^[0-9]+$ ]] && [[ "$answer" -ge 1 ]] && [[ "$answer" -le "${#options[@]}" ]]; then
      printf '%s\n' "$answer"
      return
    fi

    log_warn "Please enter a number between 1 and ${#options[@]}."
    index=1
  done
}

resolve_platform() {
  ARCH="$(uname -m)"

  case "$OS" in
    Darwin)
      case "$ARCH" in
        x86_64|amd64)
          MUTAGEN_PLATFORM="darwin_amd64"
          DOCKBRIDGE_PLATFORM="darwin_amd64"
          ;;
        arm64|aarch64)
          MUTAGEN_PLATFORM="darwin_arm64"
          DOCKBRIDGE_PLATFORM="darwin_arm64"
          ;;
        *)
          log_error "Unsupported macOS architecture: $ARCH"
          exit 1
          ;;
      esac
      ;;
    Linux)
      case "$ARCH" in
        x86_64|amd64)
          MUTAGEN_PLATFORM="linux_amd64"
          DOCKBRIDGE_PLATFORM="linux_amd64"
          ;;
        aarch64|arm64)
          MUTAGEN_PLATFORM="linux_arm64"
          DOCKBRIDGE_PLATFORM="linux_arm64"
          ;;
        *)
          log_error "Unsupported Linux architecture: $ARCH"
          exit 1
          ;;
      esac
      ;;
    *)
      log_error "Unsupported OS: $OS"
      exit 1
      ;;
  esac
}

detect_package_manager() {
  if need_command apt-get; then
    printf 'apt-get\n'
  elif need_command dnf; then
    printf 'dnf\n'
  elif need_command yum; then
    printf 'yum\n'
  elif need_command pacman; then
    printf 'pacman\n'
  elif need_command zypper; then
    printf 'zypper\n'
  else
    printf 'unknown\n'
  fi
}

install_docker_cli() {
  if need_command docker; then
    log_info "docker already installed: $(command -v docker)"
    return
  fi

  if [[ "$SKIP_DOCKER_CHECK" == "1" ]]; then
    log_warn "Skipping docker installation by request (--skip-docker-check)."
    return
  fi

  log_warn "docker command was not found."

  if [[ "$OS" == "Darwin" ]]; then
    if need_command brew; then
      if [[ "$(ask_yes_no 'Install Docker CLI using Homebrew now?')" == "0" ]]; then
        dry_run_cmd brew update
        dry_run_cmd brew install docker
        if [[ "$DRY_RUN" == "0" ]] && need_command docker; then
          log_info "Installed docker via Homebrew."
        else
          log_info "Requested docker installation via Homebrew."
        fi
      else
        log_warn "Please install docker manually from Docker Desktop or Homebrew."
      fi
    else
      log_warn "Homebrew was not found, so automatic Docker CLI installation cannot continue on macOS."
      log_warn "Please install Docker CLI from Docker Desktop or Homebrew, then rerun this script."
    fi
    return
  fi

  if [[ "$OS" == "Linux" ]]; then
    local pkg_manager
    pkg_manager="$(detect_package_manager)"

    case "$pkg_manager" in
      apt-get|dnf|yum|pacman|zypper)
        ;;
      *)
        log_warn "No supported package manager detected. Please install docker-cli via your distro package channel."
        return
        ;;
    esac

    if [[ "$(ask_yes_no "Install docker using ${pkg_manager} now?")" == "0" ]]; then
      case "$pkg_manager" in
        apt-get)
          dry_run_cmd sudo apt-get update
          dry_run_cmd sudo apt-get install -y docker.io
          ;;
        dnf)
          dry_run_cmd sudo dnf install -y docker
          ;;
        yum)
          dry_run_cmd sudo yum install -y docker
          ;;
        pacman)
          dry_run_cmd sudo pacman -Sy --noconfirm docker
          ;;
        zypper)
          dry_run_cmd sudo zypper -n install docker
          ;;
      esac
      log_info "Requested docker installation using ${pkg_manager}."
    else
      log_warn "Please install docker manually and rerun this script."
    fi
  fi
}

preferred_local_bin_dir() {
  local candidate=""

  if [[ -n "${DOCKBRIDGE_LOCAL_BIN_DIR:-}" ]]; then
    if [[ "$DRY_RUN" == "0" ]]; then
      mkdir -p "$DOCKBRIDGE_LOCAL_BIN_DIR"
    fi
    printf '%s\n' "$DOCKBRIDGE_LOCAL_BIN_DIR"
    return
  fi

  if [[ "$OS" == "Darwin" ]]; then
    for candidate in /opt/homebrew/bin /usr/local/bin; do
      if [[ -d "$candidate" && -w "$candidate" ]]; then
        printf '%s\n' "$candidate"
        return
      fi
    done
  else
    if [[ -d /usr/local/bin && -w /usr/local/bin ]]; then
      printf '/usr/local/bin\n'
      return
    fi
  fi

  if [[ "$DRY_RUN" == "0" ]]; then
    mkdir -p "$HOME/.local/bin"
  fi
  printf '%s\n' "$HOME/.local/bin"
}

latest_dockbridge_asset_url() {
  printf 'https://github.com/monlor/dockbridge/releases/latest/download/dockerbridge_%s.tar.gz\n' "$DOCKBRIDGE_PLATFORM"
}

install_local_dockbridge_binary() {
  local local_binary="${DOCKBRIDGE_LOCAL_BINARY:-$REPO_ROOT/dockerbridge}"
  local target_dir="$1"
  local target_path="$target_dir/dockerbridge"

  if [[ ! -x "$local_binary" ]]; then
    return 1
  fi

  DOCKBRIDGE_INSTALL_PATH="$target_path"
  DOCKBRIDGE_INSTALL_SOURCE="local repository binary"
  log_info "Installing dockerbridge from local repository binary."
  dry_run_cmd cp "$local_binary" "$target_path"

  if [[ "$DRY_RUN" == "1" ]]; then
    log_info "dockerbridge would be installed to $target_path"
    return 0
  fi

  chmod +x "$target_path"
  log_info "Installed dockerbridge: $target_path"
  return 0
}

install_release_dockbridge_binary() {
  local target_dir="$1"
  local target_path="$target_dir/dockerbridge"
  local version_url tmpdir install_archive extracted

  version_url="$(latest_dockbridge_asset_url)"
  DOCKBRIDGE_INSTALL_PATH="$target_path"
  DOCKBRIDGE_INSTALL_SOURCE="latest GitHub release"
  tmpdir="$(mktemp -d)"
  install_archive="$tmpdir/dockerbridge.tar.gz"

  trap 'rm -rf "$tmpdir"' RETURN

  log_info "Downloading dockerbridge release asset for $DOCKBRIDGE_PLATFORM"
  dry_run_cmd curl -fsSL -o "$install_archive" "$version_url"

  if [[ "$DRY_RUN" == "1" ]]; then
    log_info "dockerbridge would be installed to $target_path"
    trap - RETURN
    rm -rf "$tmpdir"
    return 0
  fi

  tar -xzf "$install_archive" -C "$tmpdir"
  extracted="$(find "$tmpdir" -type f -name dockerbridge | head -n 1 || true)"
  if [[ -z "$extracted" ]]; then
    log_error "Cannot locate extracted dockerbridge binary."
    trap - RETURN
    rm -rf "$tmpdir"
    exit 1
  fi

  cp "$extracted" "$target_path"
  chmod +x "$target_path"

  if [[ ! -x "$target_path" ]]; then
    log_error "Failed to install dockerbridge to $target_dir"
    trap - RETURN
    rm -rf "$tmpdir"
    exit 1
  fi

  log_info "Installed dockerbridge: $target_path"
  trap - RETURN
  rm -rf "$tmpdir"
}

install_dockbridge() {
  local target_dir

  if need_command dockerbridge; then
    DOCKBRIDGE_INSTALL_PATH="$(command -v dockerbridge)"
    DOCKBRIDGE_INSTALL_SOURCE="existing PATH command"
    log_info "dockerbridge already installed: $DOCKBRIDGE_INSTALL_PATH"
    return
  fi

  resolve_platform
  target_dir="$(preferred_local_bin_dir)"

  if install_local_dockbridge_binary "$target_dir"; then
    return
  fi

  install_release_dockbridge_binary "$target_dir"
}

latest_mutagen_asset_url() {
  local release_json asset_url
  release_json="$(
    curl -fsSL \
      -H 'Accept: application/vnd.github+json' \
      -H 'User-Agent: dockbridge-onboard' \
      https://api.github.com/repos/mutagen-io/mutagen/releases/latest
  )" || return 1

  asset_url="$(printf '%s\n' "$release_json" | sed -n "s/.*\\(\"browser_download_url\":\"[^\"]*mutagen_${MUTAGEN_PLATFORM}_v[^\"]*\\.tar\\.gz\"\\).*/\\1/p" | sed 's/^"browser_download_url":"//; s/"$//' | head -n 1)"
  if [[ -n "$asset_url" ]]; then
    printf '%s\n' "$asset_url"
    return
  fi

  asset_url="$(
    curl -fsSL https://github.com/mutagen-io/mutagen/releases/latest |
      sed -n "s/.*href=\"\\([^\"]*mutagen_${MUTAGEN_PLATFORM}_v[^\"]*\\.tar\\.gz\\)\".*/\\1/p" |
      head -n 1
  )" || return 1
  if [[ -z "$asset_url" ]]; then
    return 1
  fi

  printf 'https://github.com%s\n' "$asset_url"
}

install_mutagen() {
  if need_command mutagen; then
    log_info "mutagen already installed: $(command -v mutagen)"
    return
  fi

  if [[ "$SKIP_MUTAGEN" == "1" ]]; then
    log_warn "Skipping mutagen installation by request (--skip-mutagen)."
    return
  fi

  resolve_platform

  local version_url tmpdir install_archive extracted target_dir
  target_dir="$(preferred_local_bin_dir)"
  if [[ "$DRY_RUN" == "1" ]]; then
    log_info "Mutagen would be installed to $target_dir/mutagen"
    dry_run_cmd curl -fsSL -o "/tmp/mutagen_${MUTAGEN_PLATFORM}.tar.gz" "https://github.com/mutagen-io/mutagen/releases/latest"
    return
  fi

  version_url="$(latest_mutagen_asset_url)" || {
    log_error "Unable to resolve the latest Mutagen release URL from GitHub."
    log_error "Check network access to api.github.com or install Mutagen manually, then rerun this script."
    exit 1
  }
  tmpdir="$(mktemp -d)"
  install_archive="$tmpdir/mutagen.tar.gz"

  trap 'rm -rf "$tmpdir"' RETURN

  log_info "Downloading Mutagen for $MUTAGEN_PLATFORM"
  dry_run_cmd curl -fsSL -o "$install_archive" "$version_url"

  if [[ "$DRY_RUN" == "1" ]]; then
    log_info "Mutagen would be installed to $target_dir/mutagen"
    trap - RETURN
    rm -rf "$tmpdir"
    return
  fi

  tar -xzf "$install_archive" -C "$tmpdir"
  extracted="$(find "$tmpdir" -type f -name mutagen | head -n 1 || true)"
  if [[ -z "$extracted" ]]; then
    log_error "Cannot locate extracted mutagen binary."
    trap - RETURN
    rm -rf "$tmpdir"
    exit 1
  fi

  cp "$extracted" "$target_dir/mutagen"
  chmod +x "$target_dir/mutagen"

  if [[ ! -x "$target_dir/mutagen" ]]; then
    log_error "Failed to install mutagen to $target_dir"
    trap - RETURN
    rm -rf "$tmpdir"
    exit 1
  fi

  if [[ "$target_dir" == "$HOME/.local/bin" ]] && [[ ":$PATH:" != *":$HOME/.local/bin:"* ]]; then
    log_warn "$HOME/.local/bin is not in PATH. Add it with: export PATH=\"$HOME/.local/bin:$PATH\""
  fi

  log_info "Installed mutagen: $target_dir/mutagen"
  "$target_dir/mutagen" --version >/dev/null

  trap - RETURN
  rm -rf "$tmpdir"
}

default_shell_alias_file() {
  local shell_name="${SHELL##*/}"

  case "$shell_name" in
    zsh)
      printf '%s/.zshrc\n' "$HOME"
      ;;
    bash)
      printf '%s/.bashrc\n' "$HOME"
      ;;
    *)
      printf '%s/.profile\n' "$HOME"
      ;;
  esac
}

shell_alias_block() {
  printf '# >>> dockbridge shell alias >>>\n'
  printf "alias dockerbridge='%s'\n" "$DOCKBRIDGE_INSTALL_PATH"
  if [[ "$DOCKER_COMMAND_ALIAS_ENABLED" == "1" ]]; then
    printf "alias docker='dockerbridge'\n"
  fi
  printf '# <<< dockbridge shell alias <<<\n'
}

remove_shell_alias_block() {
  local rc_file="$1"
  local tmp_file

  tmp_file="$(mktemp)"
  awk '
    BEGIN { skip = 0 }
    $0 == "# >>> dockbridge shell alias >>>" { skip = 1; next }
    $0 == "# <<< dockbridge shell alias <<<" { skip = 0; next }
    skip == 0 { print }
  ' "$rc_file" > "$tmp_file"
  mv "$tmp_file" "$rc_file"
}

configure_dockbridge_shell_alias() {
  local alias_file alias_command

  if [[ -z "$DOCKBRIDGE_INSTALL_PATH" ]]; then
    return
  fi

  if path_contains_dir "$(dirname "$DOCKBRIDGE_INSTALL_PATH")"; then
    SHELL_ALIAS_STATUS="not needed (install directory already in PATH)"
    return
  fi

  alias_command="alias dockerbridge='$DOCKBRIDGE_INSTALL_PATH'"

  if ! shell_alias_is_interactive; then
    log_warn "$(dirname "$DOCKBRIDGE_INSTALL_PATH") is not in PATH."
    log_info "Add this command to your shell startup file:"
    printf '%s\n' "$alias_command"
    SHELL_ALIAS_STATUS="manual command printed"
    return
  fi

  if [[ "$(ask_yes_no "Configure a shell alias for dockerbridge from $(dirname "$DOCKBRIDGE_INSTALL_PATH")?")" == "1" ]]; then
    log_warn "$(dirname "$DOCKBRIDGE_INSTALL_PATH") is not in PATH. Add this manually if needed:"
    printf '%s\n' "$alias_command"
    SHELL_ALIAS_STATUS="manual command printed"
    return
  fi

  alias_file="$(default_shell_alias_file)"
  if [[ "$(ask_yes_no "Also alias docker to dockerbridge?")" == "0" ]]; then
    DOCKER_COMMAND_ALIAS_ENABLED=1
  else
    DOCKER_COMMAND_ALIAS_ENABLED=0
  fi

  if [[ "$(ask_yes_no "Write the alias to $alias_file now?")" == "0" ]]; then
    if [[ "$DRY_RUN" == "0" ]]; then
      mkdir -p "$(dirname "$alias_file")"
      touch "$alias_file"
    fi
    if [[ -f "$alias_file" ]] && grep -Fq '# >>> dockbridge shell alias >>>' "$alias_file"; then
      if [[ "$DRY_RUN" == "1" ]]; then
        dry_run_cmd sh -c "replace DockBridge shell alias block in $alias_file"
      else
        remove_shell_alias_block "$alias_file"
      fi
    fi

    if [[ "$DRY_RUN" == "1" ]]; then
      dry_run_cmd sh -c "append DockBridge shell alias block to $alias_file"
    else
      {
        printf '\n'
        shell_alias_block
      } >> "$alias_file"
    fi
    log_info "DockBridge alias stored in $alias_file"
    SHELL_ALIAS_STATUS="persisted in $alias_file"
    return
  fi

  log_info "Add this command to your shell startup file:"
  printf '%s\n' "$alias_command"
  SHELL_ALIAS_STATUS="manual command printed"
}

ssh_alias_block() {
  printf 'Host %s\n' "$SSH_ALIAS"
  printf '  HostName %s\n' "$SSH_HOST"
  printf '  User %s\n' "$SSH_USER"
  printf '  Port %s\n' "$SSH_PORT"
  if [[ -n "$SSH_KEY" ]]; then
    printf '  IdentityFile %s\n' "$SSH_KEY"
    printf '  IdentitiesOnly yes\n'
  fi
  printf '  ServerAliveInterval 30\n'
}

remove_alias_block() {
  local config_file="$1"
  local tmp_file

  tmp_file="$(mktemp)"
  awk -v alias="$SSH_ALIAS" '
    BEGIN { skip = 0 }
    $0 == "Host " alias { skip = 1; next }
    skip == 1 && /^Host [^ ]+/ { skip = 0 }
    skip == 0 { print }
  ' "$config_file" > "$tmp_file"
  mv "$tmp_file" "$config_file"
}

write_alias_block() {
  local config_file="$1"

  if [[ "$DRY_RUN" == "1" ]]; then
    dry_run_cmd sh -c "append SSH alias block for $SSH_ALIAS to $config_file"
    return
  fi

  {
    printf '\n'
    ssh_alias_block
  } >> "$config_file"
}

collect_ssh_info() {
  SERVER_NAME="$(ask 'Server name' "${SERVER_NAME:-dockbridge-remote}")"
  SERVER_NAME="$(slugify "$SERVER_NAME")"
  if [[ "$CONTEXT_NAME_EXPLICIT" == "0" ]]; then
    CONTEXT_NAME="$SERVER_NAME"
  fi

  SSH_HOST="$(ask 'Remote SSH host or IP')"
  while [[ -z "$SSH_HOST" ]]; do
    SSH_HOST="$(ask 'Remote SSH host or IP (required)')"
  done

  SSH_PORT="$(ask 'SSH port' '22')"
  SSH_USER="$(ask 'SSH user' "$USER")"
  SSH_KEY="$(expand_path "$(ask 'SSH private key path' "$HOME/.ssh/id_ed25519")")"

  if [[ ! -f "$SSH_KEY" ]]; then
    log_warn "SSH key does not exist at $SSH_KEY"
    if [[ "$(ask_yes_no 'Create a new key now?')" == "0" ]]; then
      dry_run_cmd mkdir -p "$HOME/.ssh"
      dry_run_cmd ssh-keygen -t ed25519 -f "$SSH_KEY" -N ""
      log_info "Generated new SSH key: $SSH_KEY"
      log_info "Please copy the public key to your server before continuing."
    fi
  fi

  if [[ "$(ask_yes_no 'Create / update ~/.ssh/config alias for this host?')" == "0" ]]; then
    SSH_ALIAS="$(ask 'SSH alias name' "$SERVER_NAME")"
    SSH_ALIAS="$(slugify "$SSH_ALIAS")"
  else
    SSH_ALIAS=""
  fi

  if [[ -n "$SSH_ALIAS" ]]; then
    local config_file
    config_file="$HOME/.ssh/config"
    if [[ "$DRY_RUN" == "0" ]]; then
      mkdir -p "$HOME/.ssh"
      touch "$config_file"
    fi

    if [[ -f "$config_file" ]] && grep -q "^Host $SSH_ALIAS\$" "$config_file"; then
      if [[ "$(ask_yes_no "Alias $SSH_ALIAS already exists. Overwrite block?")" == "0" ]]; then
        if [[ "$DRY_RUN" == "1" ]]; then
          dry_run_cmd sh -c "remove SSH alias block for $SSH_ALIAS from $config_file"
        else
          remove_alias_block "$config_file"
        fi
      else
        SSH_ALIAS=""
      fi
    fi

    if [[ -n "$SSH_ALIAS" ]]; then
      write_alias_block "$config_file"
      log_info "Updated SSH config alias: $SSH_ALIAS"
    fi
  fi

  if [[ "$SKIP_SSH_CHECK" == "1" ]]; then
    return
  fi

  local target
  target="$SSH_USER@$SSH_HOST"
  if [[ -n "$SSH_ALIAS" ]]; then
    target="$SSH_ALIAS"
  fi

  if ! dry_run_cmd ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=8 -i "$SSH_KEY" -p "$SSH_PORT" "$target" true; then
    log_warn "SSH check failed. If this is expected, run again with --skip-ssh-check."
    return
  fi

  log_info "SSH target reachable."
}

resolve_docker_host() {
  if [[ -n "$SSH_ALIAS" ]]; then
    DOCKER_HOST="ssh://$SSH_ALIAS"
  else
    DOCKER_HOST="ssh://$SSH_USER@$SSH_HOST:$SSH_PORT"
  fi
}

docker_context_host() {
  local context_name="$1"
  local inspect_output

  inspect_output="$(docker context inspect "$context_name" 2>/dev/null)" || return 1
  printf '%s\n' "$inspect_output" | tr -d '\n' | sed -n 's/.*"Host"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1
}

ensure_docker_for_context() {
  if need_command docker; then
    return
  fi

  if [[ "$DRY_RUN" == "1" ]]; then
    log_warn "docker is still unavailable; dry-run will print context commands without executing them."
    return
  fi

  log_error "docker is required to create a Docker context. Install Docker CLI and rerun this script."
  exit 1
}

choose_context_setup_mode() {
  local choice

  if [[ "$DRY_RUN" == "1" ]] && ! need_command docker; then
    log_warn "docker is unavailable in dry-run mode; skipping existing context discovery."
    USE_EXISTING_CONTEXT=0
    return
  fi

  ensure_docker_for_context

  choice="$(ask_choice \
    'How should DockBridge connect to Docker?' \
    'Use existing SSH Docker context' \
    'Create new SSH-backed Docker context'
  )"

  if [[ "$choice" == "1" ]]; then
    USE_EXISTING_CONTEXT=1
  else
    USE_EXISTING_CONTEXT=0
  fi
}

select_existing_ssh_context() {
  local context_names=()
  local ssh_context_names=()
  local ssh_context_hosts=()
  local context_name context_host choice index

  while IFS= read -r context_name; do
    [[ -n "$context_name" ]] || continue
    context_names+=("$context_name")
  done < <(docker context ls --format '{{.Name}}')

  for context_name in "${context_names[@]}"; do
    context_host="$(docker_context_host "$context_name" || true)"
    if [[ "$context_host" == ssh://* ]]; then
      ssh_context_names+=("$context_name")
      ssh_context_hosts+=("$context_host")
    fi
  done

  if [[ "${#ssh_context_names[@]}" -eq 0 ]]; then
    log_warn "No existing SSH Docker contexts found. Falling back to new context creation."
    USE_EXISTING_CONTEXT=0
    return 1
  fi

  printf 'Available SSH Docker contexts:\n'
  for index in "${!ssh_context_names[@]}"; do
    printf '  %s) %s -> %s\n' "$((index + 1))" "${ssh_context_names[$index]}" "${ssh_context_hosts[$index]}"
  done

  while true; do
    read -r -p "Select SSH Docker context [1-${#ssh_context_names[@]}]: " choice
    if [[ "$choice" =~ ^[0-9]+$ ]] && [[ "$choice" -ge 1 ]] && [[ "$choice" -le "${#ssh_context_names[@]}" ]]; then
      break
    fi
    log_warn "Please enter a number between 1 and ${#ssh_context_names[@]}."
  done

  CONTEXT_NAME="${ssh_context_names[$((choice - 1))]}"
  DOCKER_HOST="${ssh_context_hosts[$((choice - 1))]}"
  log_info "Selected existing SSH Docker context '$CONTEXT_NAME'."
  return 0
}

manage_context() {
  local existing=0

  ensure_docker_for_context

  if [[ "$USE_EXISTING_CONTEXT" == "1" ]]; then
    if ! select_existing_ssh_context; then
      manage_context
      return
    fi

    if [[ "$(ask_yes_no "Use context '$CONTEXT_NAME' now?")" == "0" ]]; then
      dry_run_cmd docker context use "$CONTEXT_NAME"
      log_info "Active context set to '$CONTEXT_NAME'."
    fi
    return
  fi

  if need_command docker; then
    if docker context inspect "$CONTEXT_NAME" >/dev/null 2>&1; then
      existing=1
    fi
  fi

  if [[ "$existing" -eq 1 ]]; then
    if [[ "$(ask_yes_no "Context '$CONTEXT_NAME' already exists. Recreate it?")" == "1" ]]; then
      log_info "Keeping existing context '$CONTEXT_NAME'."
      return
    fi
    dry_run_cmd docker context rm -f "$CONTEXT_NAME"
  fi

  dry_run_cmd docker context create "$CONTEXT_NAME" --docker "host=$DOCKER_HOST"

  if [[ "$(ask_yes_no "Use context '$CONTEXT_NAME' now?")" == "0" ]]; then
    dry_run_cmd docker context use "$CONTEXT_NAME"
    log_info "Active context set to '$CONTEXT_NAME'."
  fi
}

write_summary() {
  if [[ -z "$OUTPUT_SUMMARY" ]]; then
    return
  fi

  if [[ "$DRY_RUN" == "1" ]]; then
    log_info "Dry-run requested; skipping summary file write to $OUTPUT_SUMMARY"
    return
  fi

  {
    printf 'DockBridge onboarding summary\n'
    printf 'OS: %s\n' "$OS"
    printf 'Architecture: %s\n' "${ARCH:-unknown}"
    printf 'Mutagen platform: %s\n' "${MUTAGEN_PLATFORM:-unknown}"
    printf 'SSH host: %s\n' "${SSH_HOST:-not used}"
    printf 'Server name: %s\n' "${SERVER_NAME:-not used}"
    printf 'dockerbridge source: %s\n' "$DOCKBRIDGE_INSTALL_SOURCE"
    printf 'dockerbridge path: %s\n' "${DOCKBRIDGE_INSTALL_PATH:-not installed}"
    printf 'dockerbridge alias: %s\n' "$SHELL_ALIAS_STATUS"
    printf 'SSH user: %s\n' "${SSH_USER:-not used}"
    printf 'SSH port: %s\n' "${SSH_PORT:-not used}"
    if [[ -n "$SSH_ALIAS" ]]; then
      printf 'SSH alias: %s\n' "$SSH_ALIAS"
    else
      printf 'SSH alias: not used\n'
    fi
    printf 'Docker context: %s\n' "$CONTEXT_NAME"
    printf 'Docker host: %s\n' "$DOCKER_HOST"
    printf 'Mutagen skipped: %s\n' "$SKIP_MUTAGEN"
    printf 'Dry run: %s\n' "$DRY_RUN"
  } > "$OUTPUT_SUMMARY"

  log_info "Wrote summary to $OUTPUT_SUMMARY"
}

run_post_checks() {
  log_info "Post-setup checklist:"
  log_info "1) dockerbridge --version"
  log_info "2) docker context ls"
  log_info "3) docker context use $CONTEXT_NAME"
  log_info "4) docker run --rm hello-world"
  if need_command mutagen || [[ "$SKIP_MUTAGEN" == "0" ]]; then
    log_info "5) mutagen --version"
  fi

  if [[ "$SKIP_SSH_CHECK" == "0" ]]; then
    log_info "6) Quick SSH check executed during setup."
  else
    log_info "6) SSH check was skipped (--skip-ssh-check)."
  fi

  if [[ "$DRY_RUN" == "1" ]]; then
    log_info "Dry-run mode made no changes to local tools, SSH config, or Docker contexts."
  fi
}

main() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --help|-h)
        usage
        exit 0
        ;;
      --dry-run)
        DRY_RUN=1
        shift
        ;;
      --skip-mutagen)
        SKIP_MUTAGEN=1
        shift
        ;;
      --skip-docker-check)
        SKIP_DOCKER_CHECK=1
        shift
        ;;
      --skip-ssh-check)
        SKIP_SSH_CHECK=1
        shift
        ;;
      --context-name)
        CONTEXT_NAME="${2:-}"
        if [[ -z "$CONTEXT_NAME" ]]; then
          log_error "--context-name requires a value"
          exit 1
        fi
        CONTEXT_NAME_EXPLICIT=1
        shift 2
        ;;
      --output)
        OUTPUT_SUMMARY="${2:-}"
        if [[ -z "$OUTPUT_SUMMARY" ]]; then
          log_error "--output requires a file path"
          exit 1
        fi
        shift 2
        ;;
      *)
        log_error "Unknown argument: $1"
        usage
        exit 1
        ;;
    esac
  done

  if [[ "$DRY_RUN" == "1" ]]; then
    log_info "Running in dry-run mode"
  fi

  log_info "Starting DockBridge onboarding for $OS"

  assert_dependencies
  resolve_platform
  install_docker_cli
  install_mutagen
  install_dockbridge
  configure_dockbridge_shell_alias
  choose_context_setup_mode
  if [[ "$USE_EXISTING_CONTEXT" == "0" ]]; then
    collect_ssh_info
    resolve_docker_host
  fi
  manage_context
  run_post_checks
  write_summary

  log_info "DockBridge onboarding completed."
}

main "$@"

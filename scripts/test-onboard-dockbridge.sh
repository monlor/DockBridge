#!/usr/bin/env bash
set -euo pipefail
IFS=$'\n\t'

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
TARGET_SCRIPT="$ROOT_DIR/scripts/onboard-dockbridge.sh"

fail() {
  printf '[fail] %s\n' "$*" >&2
  exit 1
}

assert_contains() {
  local haystack="$1"
  local needle="$2"

  if [[ "$haystack" != *"$needle"* ]]; then
    fail "expected output to contain: $needle"
  fi
}

assert_file_missing() {
  local path="$1"

  if [[ -e "$path" ]]; then
    fail "expected file to be absent: $path"
  fi
}

assert_file_contains() {
  local path="$1"
  local needle="$2"

  if [[ ! -f "$path" ]]; then
    fail "expected file to exist: $path"
  fi

  if ! grep -Fq "$needle" "$path"; then
    fail "expected $path to contain: $needle"
  fi
}

assert_file_exists() {
  local path="$1"

  if [[ ! -f "$path" ]]; then
    fail "expected file to exist: $path"
  fi
}

assert_file_executable() {
  local path="$1"

  if [[ ! -x "$path" ]]; then
    fail "expected executable file: $path"
  fi
}

make_fake_uname() {
  local bin_dir="$1"

  cat > "$bin_dir/uname" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

case "${1:-}" in
  -s)
    printf 'Darwin\n'
    ;;
  -m)
    printf 'arm64\n'
    ;;
  *)
    printf 'Darwin\n'
    ;;
esac
EOF
  chmod +x "$bin_dir/uname"
}

make_fake_brew() {
  local bin_dir="$1"

  cat > "$bin_dir/brew" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'brew %s\n' "$*" >&2
EOF
  chmod +x "$bin_dir/brew"
}

make_fake_docker() {
  local bin_dir="$1"

  cat > "$bin_dir/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

log_path="${DOCKER_LOG:?DOCKER_LOG is required}"
printf '%s\n' "$*" >> "$log_path"

if [[ "${1:-}" == "context" && "${2:-}" == "inspect" ]]; then
  exit 1
fi

exit 0
EOF
  chmod +x "$bin_dir/docker"
}

make_fake_mutagen() {
  local bin_dir="$1"

  cat > "$bin_dir/mutagen" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'mutagen 0.0.0-test\n'
EOF
  chmod +x "$bin_dir/mutagen"
}

make_fake_dockbridge_binary() {
  local path="$1"

  cat > "$path" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'dockerbridge test binary\n'
EOF
  chmod +x "$path"
}

run_parse_test() {
  bash -n "$TARGET_SCRIPT"
}

run_dry_run_test() {
  local tmp_dir home_dir bin_dir summary_path config_path result

  tmp_dir="$(mktemp -d)"
  home_dir="$tmp_dir/home"
  bin_dir="$tmp_dir/bin"
  summary_path="$tmp_dir/summary.txt"
  config_path="$home_dir/.ssh/config"

  mkdir -p "$home_dir/.ssh" "$bin_dir"
  : > "$config_path"
  : > "$home_dir/.ssh/id_ed25519"

  make_fake_uname "$bin_dir"
  make_fake_brew "$bin_dir"

result="$(
    HOME="$home_dir" \
    PATH="$bin_dir:/usr/bin:/bin:/usr/sbin:/sbin" \
    DOCKBRIDGE_LOCAL_BIN_DIR="$home_dir/.local/bin" \
    bash "$TARGET_SCRIPT" --dry-run --output "$summary_path" <<'EOF'
y
dockbridge-example
example.com
22
tester
~/.ssh/id_ed25519
y
dockbridge-example
y
EOF
  )"

  assert_contains "$result" "[dry-run] brew update"
  assert_contains "$result" "[dry-run] brew install docker"
  assert_contains "$result" "[dry-run] docker context create dockbridge-example --docker host=ssh://dockbridge-example"
  assert_contains "$result" "[dry-run] docker context use dockbridge-example"
  assert_contains "$result" "DockBridge onboarding completed."

  if [[ -s "$config_path" ]]; then
    fail "expected dry-run to avoid modifying $config_path"
  fi

  assert_file_missing "$summary_path"
  rm -rf "$tmp_dir"
}

run_context_creation_test() {
  local tmp_dir home_dir bin_dir log_path summary_path local_binary result

  tmp_dir="$(mktemp -d)"
  home_dir="$tmp_dir/home"
  bin_dir="$tmp_dir/bin"
  log_path="$tmp_dir/docker.log"
  summary_path="$tmp_dir/summary.txt"
  local_binary="$tmp_dir/dockerbridge"

  mkdir -p "$home_dir/.ssh" "$bin_dir"
  : > "$home_dir/.ssh/id_ed25519"
  : > "$log_path"

  make_fake_uname "$bin_dir"
  make_fake_docker "$bin_dir"
  make_fake_mutagen "$bin_dir"
  make_fake_dockbridge_binary "$local_binary"

  result="$(
    HOME="$home_dir" \
    PATH="$bin_dir:/usr/bin:/bin:/usr/sbin:/sbin" \
    DOCKER_LOG="$log_path" \
    DOCKBRIDGE_LOCAL_BIN_DIR="$home_dir/.local/bin" \
    DOCKBRIDGE_LOCAL_BINARY="$local_binary" \
    bash "$TARGET_SCRIPT" --skip-docker-check --skip-mutagen --skip-ssh-check --context-name smoke-context --output "$summary_path" <<'EOF'
smoke-server
example.com
22
tester
~/.ssh/id_ed25519
n
y
EOF
  )"

  assert_contains "$result" "Active context set to 'smoke-context'."
  assert_contains "$result" "Wrote summary to $summary_path"
  assert_contains "$result" "Add this command to your shell startup file:"
  assert_contains "$result" "alias dockerbridge='$home_dir/.local/bin/dockerbridge'"
  assert_file_contains "$log_path" "context inspect smoke-context"
  assert_file_contains "$log_path" "context create smoke-context --docker host=ssh://tester@example.com:22"
  assert_file_contains "$log_path" "context use smoke-context"
  assert_file_contains "$summary_path" "Docker context: smoke-context"
  assert_file_contains "$summary_path" "Docker host: ssh://tester@example.com:22"
  assert_file_contains "$summary_path" "Server name: smoke-server"
  assert_file_contains "$summary_path" "dockerbridge source: local repository binary"
  assert_file_contains "$summary_path" "dockerbridge path: $home_dir/.local/bin/dockerbridge"
  assert_file_contains "$summary_path" "dockerbridge alias: manual command printed"
  assert_file_exists "$home_dir/.local/bin/dockerbridge"
  assert_file_executable "$home_dir/.local/bin/dockerbridge"

  rm -rf "$tmp_dir"
}

run_shell_alias_persistence_test() {
  local tmp_dir home_dir bin_dir log_path shell_rc local_binary result

  tmp_dir="$(mktemp -d)"
  home_dir="$tmp_dir/home"
  bin_dir="$tmp_dir/bin"
  log_path="$tmp_dir/docker.log"
  shell_rc="$home_dir/.zshrc"
  local_binary="$tmp_dir/dockerbridge"

  mkdir -p "$home_dir/.ssh" "$bin_dir"
  : > "$home_dir/.ssh/id_ed25519"
  : > "$log_path"

  make_fake_uname "$bin_dir"
  make_fake_docker "$bin_dir"
  make_fake_mutagen "$bin_dir"
  make_fake_dockbridge_binary "$local_binary"

  result="$(
    HOME="$home_dir" \
    PATH="$bin_dir:/usr/bin:/bin:/usr/sbin:/sbin" \
    SHELL="/bin/zsh" \
    DOCKER_LOG="$log_path" \
    DOCKBRIDGE_FORCE_INTERACTIVE="1" \
    DOCKBRIDGE_LOCAL_BIN_DIR="$home_dir/.local/bin" \
    DOCKBRIDGE_LOCAL_BINARY="$local_binary" \
    bash "$TARGET_SCRIPT" --skip-docker-check --skip-mutagen --skip-ssh-check --context-name alias-context <<'EOF'
y
y
alias-server
example.com
22
tester
~/.ssh/id_ed25519
n
y
EOF
  )"

  assert_contains "$result" "DockBridge alias stored in $shell_rc"
  assert_file_contains "$shell_rc" "# >>> dockbridge shell alias >>>"
  assert_file_contains "$shell_rc" "alias dockerbridge='$home_dir/.local/bin/dockerbridge'"
  assert_file_contains "$shell_rc" "# <<< dockbridge shell alias <<<"
  assert_file_exists "$home_dir/.local/bin/dockerbridge"
  assert_file_executable "$home_dir/.local/bin/dockerbridge"

  rm -rf "$tmp_dir"
}

run_interactive_dry_run_does_not_write_files_test() {
  local tmp_dir home_dir bin_dir shell_rc ssh_config result

  tmp_dir="$(mktemp -d)"
  home_dir="$tmp_dir/home"
  bin_dir="$tmp_dir/bin"
  shell_rc="$home_dir/.zshrc"
  ssh_config="$home_dir/.ssh/config"

  mkdir -p "$home_dir/.ssh" "$bin_dir"
  : > "$home_dir/.ssh/id_ed25519"

  make_fake_uname "$bin_dir"
  make_fake_brew "$bin_dir"

  result="$(
    HOME="$home_dir" \
    PATH="$bin_dir:/usr/bin:/bin:/usr/sbin:/sbin" \
    SHELL="/bin/zsh" \
    DOCKBRIDGE_FORCE_INTERACTIVE="1" \
    DOCKBRIDGE_LOCAL_BIN_DIR="$home_dir/.local/bin" \
    bash "$TARGET_SCRIPT" --dry-run --skip-ssh-check --output "$tmp_dir/summary.txt" <<'EOF'
y
y
y
dry-server
example.com
22
tester
~/.ssh/id_ed25519
y
dry-server
y
EOF
  )"

  assert_contains "$result" "[dry-run] sh -c append DockBridge shell alias block to $shell_rc"
  assert_contains "$result" "[dry-run] sh -c append SSH alias block for dry-server to $ssh_config"
  assert_file_missing "$shell_rc"
  assert_file_missing "$ssh_config"
  assert_file_missing "$tmp_dir/summary.txt"

  rm -rf "$tmp_dir"
}

main() {
  run_parse_test
  run_dry_run_test
  run_context_creation_test
  run_shell_alias_persistence_test
  run_interactive_dry_run_does_not_write_files_test
  printf '[pass] onboard-dockbridge smoke tests passed\n'
}

main "$@"

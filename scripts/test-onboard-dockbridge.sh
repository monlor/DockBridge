#!/usr/bin/env bash
set -euo pipefail
IFS=$'\n\t'

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
TARGET_SCRIPT="$ROOT_DIR/install.sh"

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

if [[ "${1:-}" == "context" && "${2:-}" == "ls" ]]; then
  printf '%s' "${DOCKER_CONTEXT_LS_OUTPUT:-}"
  exit 0
fi

if [[ "${1:-}" == "context" && "${2:-}" == "inspect" ]]; then
  case "${3:-}" in
    ssh-prod)
      cat <<'JSON'
[
  {
    "Endpoints": {
      "docker": {
        "Host": "ssh://prod@example.com:22"
      }
    }
  }
]
JSON
      exit 0
      ;;
    tcp-prod)
      cat <<'JSON'
[
  {
    "Endpoints": {
      "docker": {
        "Host": "tcp://prod.example.com:2376"
      }
    }
  }
]
JSON
      exit 0
      ;;
    *)
      exit 1
      ;;
  esac
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
2
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
y
2
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
  assert_file_contains "$shell_rc" "alias docker='dockerbridge'"
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

run_existing_ssh_context_selection_test() {
  local tmp_dir home_dir bin_dir log_path summary_path local_binary result ssh_config

  tmp_dir="$(mktemp -d)"
  home_dir="$tmp_dir/home"
  bin_dir="$tmp_dir/bin"
  log_path="$tmp_dir/docker.log"
  summary_path="$tmp_dir/summary.txt"
  local_binary="$tmp_dir/dockerbridge"
  ssh_config="$home_dir/.ssh/config"

  mkdir -p "$home_dir/.ssh" "$bin_dir"
  : > "$home_dir/.ssh/id_ed25519"
  : > "$log_path"
  : > "$ssh_config"

  make_fake_uname "$bin_dir"
  make_fake_docker "$bin_dir"
  make_fake_mutagen "$bin_dir"
  make_fake_dockbridge_binary "$local_binary"

  result="$(
    HOME="$home_dir" \
    PATH="$bin_dir:/usr/bin:/bin:/usr/sbin:/sbin" \
    DOCKER_LOG="$log_path" \
    DOCKER_CONTEXT_LS_OUTPUT=$'ssh-prod\ntcp-prod\n' \
    DOCKBRIDGE_LOCAL_BIN_DIR="$home_dir/.local/bin" \
    DOCKBRIDGE_LOCAL_BINARY="$local_binary" \
    bash "$TARGET_SCRIPT" --skip-docker-check --skip-mutagen --output "$summary_path" <<'EOF'
1
1
y
EOF
  )"

  assert_contains "$result" "Available SSH Docker contexts:"
  assert_contains "$result" "1) ssh-prod -> ssh://prod@example.com:22"
  assert_contains "$result" "Selected existing SSH Docker context 'ssh-prod'."
  assert_file_contains "$log_path" "context ls"
  assert_file_contains "$log_path" "context inspect ssh-prod"
  assert_file_contains "$log_path" "context inspect tcp-prod"
  assert_file_contains "$log_path" "context use ssh-prod"
  if grep -Fq "context create" "$log_path"; then
    fail "expected existing context selection to skip context creation"
  fi
  assert_file_contains "$summary_path" "Docker context: ssh-prod"
  assert_file_contains "$summary_path" "Docker host: ssh://prod@example.com:22"
  if [[ -s "$ssh_config" ]]; then
    fail "expected existing context selection to skip SSH config updates"
  fi

  rm -rf "$tmp_dir"
}

run_stdin_script_uses_separate_prompt_fd_test() {
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
    DOCKER_CONTEXT_LS_OUTPUT=$'ssh-prod\ntcp-prod\n' \
    DOCKBRIDGE_LOCAL_BIN_DIR="$bin_dir" \
    DOCKBRIDGE_LOCAL_BINARY="$local_binary" \
    DOCKBRIDGE_PROMPT_FD="3" \
    perl -e 'alarm 5; exec @ARGV' bash -s -- --skip-docker-check --skip-mutagen --output "$summary_path" 3<<'EOF_INPUT' <<EOF_SCRIPT
1
1
y
EOF_INPUT
$(cat "$TARGET_SCRIPT")
EOF_SCRIPT
  )"

  assert_contains "$result" "Selected existing SSH Docker context 'ssh-prod'."
  assert_file_contains "$log_path" "context use ssh-prod"
  assert_file_contains "$summary_path" "Docker context: ssh-prod"

  rm -rf "$tmp_dir"
}

run_stdin_script_keeps_prompt_output_visible_test() {
  local tmp_dir home_dir bin_dir log_path local_binary output_path

  tmp_dir="$(mktemp -d)"
  home_dir="$tmp_dir/home"
  bin_dir="$tmp_dir/bin"
  log_path="$tmp_dir/docker.log"
  local_binary="$tmp_dir/dockerbridge"
  output_path="$tmp_dir/output.txt"

  mkdir -p "$home_dir/.ssh" "$bin_dir"
  : > "$home_dir/.ssh/id_ed25519"
  : > "$log_path"

  make_fake_uname "$bin_dir"
  make_fake_docker "$bin_dir"
  make_fake_mutagen "$bin_dir"
  make_fake_dockbridge_binary "$local_binary"

  set +e
  HOME="$home_dir" \
    PATH="$bin_dir:/usr/bin:/bin:/usr/sbin:/sbin" \
    DOCKER_LOG="$log_path" \
    DOCKBRIDGE_LOCAL_BIN_DIR="$bin_dir" \
    DOCKBRIDGE_LOCAL_BINARY="$local_binary" \
    script -q "$tmp_dir/typescript.log" \
      bash -lc "timeout 3s bash -s -- --skip-docker-check --skip-mutagen < '$TARGET_SCRIPT'" \
      > "$output_path" 2>&1
  set -e

  assert_file_contains "$output_path" "How should DockBridge connect to Docker?"
  assert_file_contains "$output_path" "Choose [1-2]:"

  rm -rf "$tmp_dir"
}

main() {
  run_parse_test
  run_dry_run_test
  run_context_creation_test
  run_shell_alias_persistence_test
  run_interactive_dry_run_does_not_write_files_test
  run_existing_ssh_context_selection_test
  run_stdin_script_uses_separate_prompt_fd_test
  run_stdin_script_keeps_prompt_output_visible_test
  printf '[pass] install.sh smoke tests passed\n'
}

main "$@"

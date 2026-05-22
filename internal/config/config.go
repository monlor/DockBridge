package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	RemoteDockerHost    string
	RealDockerPath      string
	StateDir            string
	RemoteWorkspaceRoot string
	LocalBindAddress    string
	RemotePortStart     int
	MutagenPath         string
	MutagenMode         string
	MutagenIgnores      []string
}

func (c Config) IsSSHRemote() bool {
	return strings.HasPrefix(c.RemoteDockerHost, "ssh://")
}

type Options struct {
	Env         map[string]string
	HomeDir     string
	Lookup      func(string) (string, error)
	ContextHost func(realDocker string) (string, error)
	RemoteHome  func(remoteHost string) (string, error)
}

func Load(opts Options) (Config, error) {
	env := opts.Env
	if env == nil {
		env = environ()
	}
	home := opts.HomeDir
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = h
		}
	}
	lookup := opts.Lookup
	if lookup == nil {
		lookup = exec.LookPath
	}
	contextHost := opts.ContextHost
	if contextHost == nil {
		contextHost = currentDockerContextHost
	}
	remoteHome := opts.RemoteHome
	if remoteHome == nil {
		remoteHome = currentRemoteHome
	}
	defaultLookup := opts.Lookup == nil

	realDocker := env["DOCKBRIDGE_REAL_DOCKER"]
	if realDocker == "" {
		dockerPath, err := lookup("docker")
		if err != nil {
			return Config{}, errors.New("real docker binary not found; set DOCKBRIDGE_REAL_DOCKER")
		}
		if defaultLookup {
			if self, err := os.Executable(); err == nil && sameFile(dockerPath, self) {
				dockerPath, err = lookPathSkippingSelf("docker", self, os.Getenv("PATH"))
				if err != nil {
					return Config{}, errors.New("real docker binary not found after DockBridge shim; set DOCKBRIDGE_REAL_DOCKER")
				}
			}
		}
		realDocker = dockerPath
	}

	remote := firstNonEmpty(env["DOCKBRIDGE_REMOTE"], env["DOCKER_HOST"])
	if remote == "" {
		if contextRemote, err := contextHost(realDocker); err == nil {
			remote = contextRemote
		}
	}
	if remote != "" && !strings.Contains(remote, "://") {
		remote = "ssh://" + remote
	}

	stateDir := env["DOCKBRIDGE_STATE_DIR"]
	if stateDir == "" {
		stateDir = filepath.Join(home, ".dockbridge")
	}

	remoteRoot := env["DOCKBRIDGE_REMOTE_WORKSPACE_ROOT"]
	if remoteRoot == "" {
		remoteRoot = defaultRemoteWorkspaceRoot(remote, remoteHome)
	} else if expanded, ok := expandRemoteHome(remoteRoot, remote, remoteHome); ok {
		remoteRoot = expanded
	}

	bind := env["DOCKBRIDGE_LOCAL_BIND"]
	if bind == "" {
		bind = "127.0.0.1"
	}
	remotePortStart := 49152
	if value := env["DOCKBRIDGE_REMOTE_PORT_START"]; value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, errors.New("DOCKBRIDGE_REMOTE_PORT_START must be an integer")
		}
		remotePortStart = parsed
	}
	mutagenPath := env["DOCKBRIDGE_MUTAGEN_BIN"]
	if mutagenPath == "" {
		mutagenPath = "mutagen"
	}
	mutagenMode := env["DOCKBRIDGE_MUTAGEN_MODE"]
	if mutagenMode == "" {
		mutagenMode = "one-way-replica"
	}
	mutagenIgnores := splitList(env["DOCKBRIDGE_MUTAGEN_IGNORE"])

	return Config{
		RemoteDockerHost:    remote,
		RealDockerPath:      realDocker,
		StateDir:            stateDir,
		RemoteWorkspaceRoot: remoteRoot,
		LocalBindAddress:    bind,
		RemotePortStart:     remotePortStart,
		MutagenPath:         mutagenPath,
		MutagenMode:         mutagenMode,
		MutagenIgnores:      mutagenIgnores,
	}, nil
}

func environ() map[string]string {
	out := make(map[string]string)
	for _, kv := range os.Environ() {
		key, value, ok := strings.Cut(kv, "=")
		if ok {
			out[key] = value
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func splitList(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func currentDockerContextHost(realDocker string) (string, error) {
	out, err := exec.Command(realDocker, "context", "inspect", "--format", "{{json .Endpoints.docker.Host}}").Output()
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(string(out))
	var host string
	if err := json.Unmarshal([]byte(trimmed), &host); err == nil {
		return host, nil
	}
	return strings.Trim(trimmed, `"`), nil
}

func lookPathSkippingSelf(name, self, pathEnv string) (string, error) {
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		if sameFile(candidate, self) {
			continue
		}
		return candidate, nil
	}
	return "", exec.ErrNotFound
}

func sameFile(a, b string) bool {
	aInfo, aErr := os.Stat(a)
	bInfo, bErr := os.Stat(b)
	if aErr != nil || bErr != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return os.SameFile(aInfo, bInfo)
}

func defaultRemoteWorkspaceRoot(remoteHost string, remoteHome func(string) (string, error)) string {
	if home, err := remoteHome(remoteHost); err == nil && home != "" {
		return path.Join(home, ".dockbridge", "workspaces")
	}
	return "/tmp/dockbridge/workspaces"
}

func expandRemoteHome(remotePath, remoteHost string, remoteHome func(string) (string, error)) (string, bool) {
	if remotePath != "~" && !strings.HasPrefix(remotePath, "~/") {
		return "", false
	}
	home, err := remoteHome(remoteHost)
	if err != nil || home == "" {
		return "", false
	}
	if remotePath == "~" {
		return home, true
	}
	return path.Join(home, strings.TrimPrefix(remotePath, "~/")), true
}

func currentRemoteHome(remoteHost string) (string, error) {
	if remoteHost == "" {
		return "", errors.New("remote host is required")
	}
	target, port, err := sshTarget(remoteHost)
	if err != nil {
		return "", err
	}
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=5"}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, target, "sh", "-lc", `printf %s "$HOME"`)
	out, err := exec.Command("ssh", args...).Output()
	if err != nil {
		return "", err
	}
	home := strings.TrimSpace(string(out))
	if home == "" || !strings.HasPrefix(home, "/") {
		return "", fmt.Errorf("remote home %q is not an absolute path", home)
	}
	return home, nil
}

func sshTarget(remoteHost string) (string, string, error) {
	if strings.HasPrefix(remoteHost, "ssh://") {
		parsed, err := url.Parse(remoteHost)
		if err != nil {
			return "", "", err
		}
		host := parsed.Hostname()
		if host == "" {
			return "", "", fmt.Errorf("invalid remote host %q", remoteHost)
		}
		if parsed.User != nil {
			host = parsed.User.String() + "@" + host
		}
		return host, parsed.Port(), nil
	}
	target := remoteHost
	port := ""
	if at := strings.LastIndex(remoteHost, "@"); at >= 0 {
		hostPart := remoteHost[at+1:]
		if host, parsedPort, ok := strings.Cut(hostPart, ":"); ok && parsedPort != "" {
			target = remoteHost[:at+1] + host
			port = parsedPort
		}
	} else if host, parsedPort, ok := strings.Cut(remoteHost, ":"); ok && parsedPort != "" {
		target = host
		port = parsedPort
	}
	if target == "" {
		return "", "", fmt.Errorf("invalid remote host %q", remoteHost)
	}
	return target, port, nil
}

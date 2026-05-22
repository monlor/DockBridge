package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigFromEnv(t *testing.T) {
	cfg, err := Load(Options{
		Env: map[string]string{
			"DOCKBRIDGE_REMOTE":                "dev@example.com",
			"DOCKBRIDGE_REAL_DOCKER":           "/usr/local/bin/docker-real",
			"DOCKBRIDGE_MUTAGEN_BIN":           "/opt/homebrew/bin/mutagen",
			"DOCKBRIDGE_MUTAGEN_MODE":          "two-way-resolved",
			"DOCKBRIDGE_MUTAGEN_IGNORE":        "node_modules,.git,tmp/cache",
			"DOCKBRIDGE_STATE_DIR":             "/tmp/state",
			"DOCKBRIDGE_REMOTE_WORKSPACE_ROOT": "/srv/dockbridge",
			"DOCKBRIDGE_REMOTE_PORT_START":     "50000",
		},
		HomeDir: "/Users/me",
		Lookup:  func(string) (string, error) { return "", errors.New("unused") },
	})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.RemoteDockerHost != "ssh://dev@example.com" {
		t.Fatalf("RemoteDockerHost = %q", cfg.RemoteDockerHost)
	}
	if cfg.RealDockerPath != "/usr/local/bin/docker-real" {
		t.Fatalf("RealDockerPath = %q", cfg.RealDockerPath)
	}
	if cfg.MutagenPath != "/opt/homebrew/bin/mutagen" {
		t.Fatalf("MutagenPath = %q", cfg.MutagenPath)
	}
	if cfg.MutagenMode != "two-way-resolved" {
		t.Fatalf("MutagenMode = %q", cfg.MutagenMode)
	}
	wantIgnores := []string{"node_modules", ".git", "tmp/cache"}
	if !equalSlices(cfg.MutagenIgnores, wantIgnores) {
		t.Fatalf("MutagenIgnores = %#v, want %#v", cfg.MutagenIgnores, wantIgnores)
	}
	if cfg.StateDir != "/tmp/state" {
		t.Fatalf("StateDir = %q", cfg.StateDir)
	}
	if cfg.RemoteWorkspaceRoot != "/srv/dockbridge" {
		t.Fatalf("RemoteWorkspaceRoot = %q", cfg.RemoteWorkspaceRoot)
	}
	if cfg.RemotePortStart != 50000 {
		t.Fatalf("RemotePortStart = %d", cfg.RemotePortStart)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := Load(Options{
		Env:     map[string]string{},
		HomeDir: "/Users/me",
		Lookup: func(name string) (string, error) {
			if name == "docker" {
				return "/opt/homebrew/bin/docker", nil
			}
			return "", errors.New("not found")
		},
		ContextHost: func(string) (string, error) { return "", nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.StateDir != filepath.Join("/Users/me", ".dockbridge") {
		t.Fatalf("StateDir = %q", cfg.StateDir)
	}
	if cfg.RemoteWorkspaceRoot != "/tmp/dockbridge/workspaces" {
		t.Fatalf("RemoteWorkspaceRoot = %q", cfg.RemoteWorkspaceRoot)
	}
	if cfg.RealDockerPath != "/opt/homebrew/bin/docker" {
		t.Fatalf("RealDockerPath = %q", cfg.RealDockerPath)
	}
	if cfg.MutagenPath != "mutagen" {
		t.Fatalf("MutagenPath = %q", cfg.MutagenPath)
	}
	if cfg.MutagenMode != "one-way-replica" {
		t.Fatalf("MutagenMode = %q", cfg.MutagenMode)
	}
	if cfg.RemotePortStart != 49152 {
		t.Fatalf("RemotePortStart = %d", cfg.RemotePortStart)
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestLoadConfigUsesCurrentDockerContext(t *testing.T) {
	cfg, err := Load(Options{
		Env:     map[string]string{},
		HomeDir: "/Users/me",
		Lookup:  func(string) (string, error) { return "/bin/docker", nil },
		ContextHost: func(realDocker string) (string, error) {
			if realDocker != "/bin/docker" {
				t.Fatalf("ContextHost realDocker = %q", realDocker)
			}
			return "ssh://monlor@nuc.monlor.cn:1023", nil
		},
		RemoteHome: func(remoteHost string) (string, error) {
			if remoteHost != "ssh://monlor@nuc.monlor.cn:1023" {
				t.Fatalf("RemoteHome remoteHost = %q", remoteHost)
			}
			return "/Users/monlor", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RemoteDockerHost != "ssh://monlor@nuc.monlor.cn:1023" {
		t.Fatalf("RemoteDockerHost = %q", cfg.RemoteDockerHost)
	}
	if cfg.RemoteWorkspaceRoot != "/Users/monlor/.dockbridge/workspaces" {
		t.Fatalf("RemoteWorkspaceRoot = %q", cfg.RemoteWorkspaceRoot)
	}
}

func TestLoadConfigKeepsNonSSHContextHost(t *testing.T) {
	cfg, err := Load(Options{
		Env:     map[string]string{},
		HomeDir: "/Users/me",
		Lookup:  func(string) (string, error) { return "/bin/docker", nil },
		ContextHost: func(realDocker string) (string, error) {
			if realDocker != "/bin/docker" {
				t.Fatalf("ContextHost realDocker = %q", realDocker)
			}
			return "tcp://docker.example.com:2376", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RemoteDockerHost != "tcp://docker.example.com:2376" {
		t.Fatalf("RemoteDockerHost = %q", cfg.RemoteDockerHost)
	}
	if cfg.IsSSHRemote() {
		t.Fatal("non-ssh context should not be marked as ssh")
	}
	if cfg.RemoteWorkspaceRoot != "/tmp/dockbridge/workspaces" {
		t.Fatalf("RemoteWorkspaceRoot = %q", cfg.RemoteWorkspaceRoot)
	}
}

func TestLoadConfigEnvRemoteOverridesCurrentDockerContext(t *testing.T) {
	cfg, err := Load(Options{
		Env: map[string]string{
			"DOCKER_HOST": "ssh://from-env",
		},
		HomeDir:     "/Users/me",
		Lookup:      func(string) (string, error) { return "/bin/docker", nil },
		ContextHost: func(string) (string, error) { return "ssh://from-context", nil },
		RemoteHome:  func(string) (string, error) { return "/home/from-env", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RemoteDockerHost != "ssh://from-env" {
		t.Fatalf("RemoteDockerHost = %q", cfg.RemoteDockerHost)
	}
}

func TestLoadConfigExpandsTildeRemoteWorkspaceRoot(t *testing.T) {
	cfg, err := Load(Options{
		Env: map[string]string{
			"DOCKER_HOST":                      "ssh://me@example.com:2222",
			"DOCKBRIDGE_REMOTE_WORKSPACE_ROOT": "~/.dockbridge/workspaces",
		},
		HomeDir:     "/Users/me",
		Lookup:      func(string) (string, error) { return "/bin/docker", nil },
		ContextHost: func(string) (string, error) { return "ssh://from-context", nil },
		RemoteHome: func(remoteHost string) (string, error) {
			if remoteHost != "ssh://me@example.com:2222" {
				t.Fatalf("RemoteHome remoteHost = %q", remoteHost)
			}
			return "/Users/remote-me", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RemoteWorkspaceRoot != "/Users/remote-me/.dockbridge/workspaces" {
		t.Fatalf("RemoteWorkspaceRoot = %q", cfg.RemoteWorkspaceRoot)
	}
}

func TestLoadConfigErrorsWhenDockerMissing(t *testing.T) {
	_, err := Load(Options{
		Env:     map[string]string{},
		HomeDir: "/Users/me",
		Lookup:  func(string) (string, error) { return "", errors.New("not found") },
	})
	if err == nil {
		t.Fatal("expected missing docker error")
	}
}

func TestLoadConfigErrorsWhenRemotePortStartInvalid(t *testing.T) {
	_, err := Load(Options{
		Env: map[string]string{
			"DOCKBRIDGE_REAL_DOCKER":       "/bin/docker",
			"DOCKBRIDGE_REMOTE_PORT_START": "not-a-port",
		},
		HomeDir: "/Users/me",
	})
	if err == nil {
		t.Fatal("expected invalid remote port start error")
	}
}

func TestLoadConfigReadsProcessEnvironmentWhenEnvNil(t *testing.T) {
	t.Setenv("DOCKBRIDGE_REMOTE", "env@example.com")
	cfg, err := Load(Options{
		Env:     nil,
		HomeDir: "/Users/me",
		Lookup:  func(string) (string, error) { return "/bin/docker", nil },
		RemoteHome: func(remoteHost string) (string, error) {
			if remoteHost != "ssh://env@example.com" {
				t.Fatalf("RemoteHome remoteHost = %q", remoteHost)
			}
			return "/home/env", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RemoteDockerHost != "ssh://env@example.com" {
		t.Fatalf("RemoteDockerHost = %q", cfg.RemoteDockerHost)
	}
}

func TestRemoteWorkspaceRootHelpers(t *testing.T) {
	remoteHome := func(remoteHost string) (string, error) {
		if remoteHost != "ssh://me@example.com:2222" {
			t.Fatalf("remoteHost = %q", remoteHost)
		}
		return "/Users/remote", nil
	}
	root := defaultRemoteWorkspaceRoot("ssh://me@example.com:2222", remoteHome)
	if root != "/Users/remote/.dockbridge/workspaces" {
		t.Fatalf("defaultRemoteWorkspaceRoot = %q", root)
	}
	expanded, ok := expandRemoteHome("~/custom", "ssh://me@example.com:2222", remoteHome)
	if !ok || expanded != "/Users/remote/custom" {
		t.Fatalf("expandRemoteHome = %q, %v", expanded, ok)
	}
	if _, ok := expandRemoteHome("/absolute", "ssh://me@example.com:2222", remoteHome); ok {
		t.Fatal("absolute path should not expand")
	}
	fallback := defaultRemoteWorkspaceRoot("ssh://me@example.com:2222", func(string) (string, error) {
		return "", errors.New("no home")
	})
	if fallback != "/tmp/dockbridge/workspaces" {
		t.Fatalf("fallback root = %q", fallback)
	}
}

func TestSSHTargetParsesDockerSSHHosts(t *testing.T) {
	target, port, err := sshTarget("ssh://me@example.com:2222")
	if err != nil {
		t.Fatal(err)
	}
	if target != "me@example.com" || port != "2222" {
		t.Fatalf("sshTarget = %q, %q", target, port)
	}
	target, port, err = sshTarget("me@example.com:2200")
	if err != nil {
		t.Fatal(err)
	}
	if target != "me@example.com" || port != "2200" {
		t.Fatalf("sshTarget scp style = %q, %q", target, port)
	}
	if _, _, err := sshTarget("ssh://"); err == nil {
		t.Fatal("expected invalid ssh target error")
	}
}

func TestCurrentDockerContextHostParsesJSONOutput(t *testing.T) {
	dir := t.TempDir()
	docker := filepath.Join(dir, "docker")
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nprintf '\"ssh://me@example.com:2222\"\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	host, err := currentDockerContextHost(docker)
	if err != nil {
		t.Fatal(err)
	}
	if host != "ssh://me@example.com:2222" {
		t.Fatalf("currentDockerContextHost = %q", host)
	}
}

func TestLookPathSkippingSelfFindsNextDocker(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	second := filepath.Join(dir, "second")
	if err := os.Mkdir(first, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(second, 0o700); err != nil {
		t.Fatal(err)
	}
	self := filepath.Join(first, "docker")
	real := filepath.Join(second, "docker")
	for _, path := range []string{self, real} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	got, err := lookPathSkippingSelf("docker", self, first+string(os.PathListSeparator)+second)
	if err != nil {
		t.Fatal(err)
	}
	if got != real {
		t.Fatalf("lookPathSkippingSelf = %q, want %q", got, real)
	}
}

func TestCurrentRemoteHomeUsesSSH(t *testing.T) {
	dir := t.TempDir()
	ssh := filepath.Join(dir, "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\nprintf '/Users/remote\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	home, err := currentRemoteHome("ssh://me@example.com:2222")
	if err != nil {
		t.Fatal(err)
	}
	if home != "/Users/remote" {
		t.Fatalf("currentRemoteHome = %q", home)
	}
}

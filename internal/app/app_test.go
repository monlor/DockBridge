package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"dockbridge/internal/config"
	"dockbridge/internal/ports"
	"dockbridge/internal/session"
	dbsync "dockbridge/internal/sync"
)

func TestBypassDelegatesWithoutSideEffects(t *testing.T) {
	exec := &RecordingExecutor{}
	a := App{Executor: exec}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"ps"},
		Env:        map[string]string{"DOCKBRIDGE_BYPASS": "1"},
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: "/srv/dockbridge",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(exec.Calls) != 1 || exec.Calls[0].Name != "/bin/docker" || strings.Join(exec.Calls[0].Args, " ") != "ps" {
		t.Fatalf("unexpected executor calls: %+v", exec.Calls)
	}
	if exec.SideEffects != 0 {
		t.Fatalf("bypass should not create side effects")
	}
}

func TestPassthroughUsesRemoteDockerHost(t *testing.T) {
	exec := &RecordingExecutor{}
	a := App{Executor: exec}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"ps"},
		Env:        map[string]string{},
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: "/srv/dockbridge",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(exec.Calls[0].Args, " ")
	if got != "-H ssh://dev ps" {
		t.Fatalf("args = %q", got)
	}
}

func TestDockerContextCommandUsesLocalDockerCLI(t *testing.T) {
	exec := &RecordingExecutor{}
	a := App{Executor: exec}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"context", "show"},
		Env:        map[string]string{},
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: "/srv/dockbridge",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(exec.Calls[0].Args, " ")
	if got != "context show" {
		t.Fatalf("context command args = %q", got)
	}
}

func TestUnsupportedTranslatedCommandFailsBeforeDocker(t *testing.T) {
	exec := &RecordingExecutor{}
	a := App{Executor: exec}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"buildx", "bake"},
		Env:        map[string]string{},
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: "/srv/dockbridge",
		},
	})
	if err == nil {
		t.Fatal("expected unsupported command error")
	}
	if len(exec.Calls) != 0 {
		t.Fatalf("unsupported command should not call docker: %+v", exec.Calls)
	}
}

func TestMissingRequiredRuntimeConfig(t *testing.T) {
	a := App{Executor: &RecordingExecutor{}}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"ps"},
		Env:        map[string]string{},
		Config:     config.Config{RemoteDockerHost: "ssh://dev"},
	})
	if err == nil || !strings.Contains(err.Error(), "real docker path") {
		t.Fatalf("expected real docker path error, got %v", err)
	}

	err = a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"ps"},
		Env:        map[string]string{},
		Config:     config.Config{RealDockerPath: "/bin/docker"},
	})
	if err == nil || !strings.Contains(err.Error(), "remote Docker host") {
		t.Fatalf("expected remote Docker host error, got %v", err)
	}
}

func TestDockerRunTranslatesMountsAndPorts(t *testing.T) {
	cwd := t.TempDir()
	remote := t.TempDir()
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exec := &RecordingExecutor{}
	tunnels := ports.NewMemoryTunnelManager()
	a := App{
		Executor:        exec,
		SyncDriver:      dbsync.LocalMirrorDriver{},
		TunnelStarter:   tunnels,
		SessionStore:    session.NewStore(state),
		RemotePortStart: 49152,
		PortAvailable:   func(string, int) bool { return true },
	}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"run", "-v", ".:/app", "-p", "3000:3000", "nginx"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			StateDir:            state,
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: remote,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(exec.Calls[0].Args, " ")
	if !strings.Contains(got, "-H ssh://dev run") || !strings.Contains(got, remote) || !strings.Contains(got, "127.0.0.1:49152:3000") {
		t.Fatalf("translated docker run args mismatch: %s", got)
	}
	if tunnels.ActiveCount() != 0 {
		t.Fatalf("foreground command should stop tunnels when docker exits")
	}
}

func TestDockerRunRejectsPathLikePublishBeforeDocker(t *testing.T) {
	exec := &RecordingExecutor{}
	a := App{
		Executor:        exec,
		TunnelStarter:   ports.NewMemoryTunnelManager(),
		RemoteValidator: failingValidator{},
		PortAvailable:   func(string, int) bool { return true },
	}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"run", "-p", "8080:80", "-p", "/Users/monlor/Downloads:/tmp", "nginx"},
		Env:        map[string]string{},
		Cwd:        t.TempDir(),
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: "/remote",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "use -v/--volume") {
		t.Fatalf("expected path-like publish error, got %v", err)
	}
	if strings.Contains(err.Error(), "remote Docker connectivity failed") {
		t.Fatalf("publish validation should happen before remote validation, got %v", err)
	}
	if len(exec.Calls) != 0 {
		t.Fatalf("docker should not run for invalid publish: %+v", exec.Calls)
	}
}

func TestDetachedDockerRunKeepsTunnelForSessionManager(t *testing.T) {
	cwd := t.TempDir()
	remote := t.TempDir()
	state := t.TempDir()
	exec := &RecordingExecutor{}
	tunnels := ports.NewMemoryTunnelManager()
	a := App{
		Executor:        exec,
		SyncDriver:      dbsync.LocalMirrorDriver{},
		TunnelStarter:   tunnels,
		SessionStore:    session.NewStore(state),
		RemotePortStart: 49152,
		PortAvailable:   func(string, int) bool { return true },
	}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"run", "-d", "-v", ".:/app", "-p", "3000:80", "nginx"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			StateDir:            state,
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: remote,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tunnels.ActiveCount() != 1 {
		t.Fatalf("detached command should keep tunnel for session management")
	}
	id := session.Identity(cwd, "ssh://dev", filepath.Base(cwd))
	loaded, err := session.NewStore(state).Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Tunnels) != 1 || loaded.Tunnels[0].LocalPort != 3000 || loaded.Tunnels[0].RemotePort != 49152 {
		t.Fatalf("tunnel state not persisted: %+v", loaded.Tunnels)
	}
	if len(loaded.Syncs) != 1 || !loaded.Syncs[0].Active {
		t.Fatalf("detached command should persist active sync state: %+v", loaded.Syncs)
	}
}

func TestDockerStopSuspendsManagedTunnelState(t *testing.T) {
	cwd := t.TempDir()
	remote := t.TempDir()
	state := t.TempDir()
	exec := &RecordingExecutor{}
	tunnels := ports.NewMemoryTunnelManager()
	store := session.NewStore(state)
	a := App{
		Executor:        exec,
		TunnelStarter:   tunnels,
		SessionStore:    store,
		RemotePortStart: 49152,
		PortAvailable:   func(string, int) bool { return true },
	}
	cfg := config.Config{
		RealDockerPath:      "/bin/docker",
		RemoteDockerHost:    "ssh://dev",
		StateDir:            state,
		LocalBindAddress:    "127.0.0.1",
		RemoteWorkspaceRoot: remote,
	}
	if err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"run", "-d", "-p", "3000:80", "nginx"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config:     cfg,
	}); err != nil {
		t.Fatal(err)
	}
	if tunnels.ActiveCount() != 1 {
		t.Fatalf("expected active tunnel after detached run")
	}
	if err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"stop", "container-id"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config:     cfg,
	}); err != nil {
		t.Fatal(err)
	}
	if tunnels.ActiveCount() != 0 {
		t.Fatalf("stop should clean managed tunnel")
	}
	id := session.Identity(cwd, "ssh://dev", filepath.Base(cwd))
	loaded, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Tunnels) != 1 || loaded.Tunnels[0].Active {
		t.Fatalf("managed tunnel state should be inactive after stop: %+v", loaded)
	}
	if len(loaded.Syncs) != 0 {
		t.Fatalf("unexpected sync state after stop: %+v", loaded.Syncs)
	}
}

func TestDockerStopSuspendsManagedSyncState(t *testing.T) {
	cwd := t.TempDir()
	remote := t.TempDir()
	state := t.TempDir()
	exec := &RecordingExecutor{}
	tunnels := ports.NewMemoryTunnelManager()
	store := session.NewStore(state)
	syncDriver := &recordingSyncDriver{status: dbsync.Status{
		Backend:           "mutagen",
		MutagenName:       "dockbridge-sync",
		MutagenIdentifier: "sync_abc",
		RemoteEndpoint:    "dev:/remote",
		LastStatus:        "Watching for changes",
		Active:            true,
	}}
	a := App{
		Executor:        exec,
		SyncDriver:      syncDriver,
		TunnelStarter:   tunnels,
		SessionStore:    store,
		RemotePortStart: 49152,
		PortAvailable:   func(string, int) bool { return true },
	}
	cfg := config.Config{
		RealDockerPath:      "/bin/docker",
		RemoteDockerHost:    "ssh://dev",
		StateDir:            state,
		LocalBindAddress:    "127.0.0.1",
		RemoteWorkspaceRoot: remote,
		MutagenPath:         "/usr/bin/true",
	}
	if err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"run", "-d", "-v", ".:/app", "-p", "3000:80", "nginx"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config:     cfg,
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"stop", "container-id"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config:     cfg,
	}); err != nil {
		t.Fatal(err)
	}
	id := session.Identity(cwd, "ssh://dev", filepath.Base(cwd))
	loaded, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Syncs) != 1 || loaded.Syncs[0].Active {
		t.Fatalf("managed sync should be inactive after stop: %+v", loaded.Syncs)
	}
	if len(loaded.Tunnels) != 1 || loaded.Tunnels[0].Active {
		t.Fatalf("managed tunnel should be inactive after stop: %+v", loaded.Tunnels)
	}
}

func TestDockerStartRestoresManagedSession(t *testing.T) {
	cwd := t.TempDir()
	remote := t.TempDir()
	state := t.TempDir()
	exec := &RecordingExecutor{}
	tunnels := ports.NewMemoryTunnelManager()
	store := session.NewStore(state)
	syncDriver := &recordingSyncDriver{status: dbsync.Status{
		Backend:           "mutagen",
		MutagenName:       "dockbridge-sync",
		MutagenIdentifier: "sync_abc",
		RemoteEndpoint:    "dev:/remote",
		LastStatus:        "Watching for changes",
		Active:            true,
	}}
	a := App{
		Executor:        exec,
		SyncDriver:      syncDriver,
		TunnelStarter:   tunnels,
		SessionStore:    store,
		RemotePortStart: 49152,
		PortAvailable:   func(string, int) bool { return true },
	}
	cfg := config.Config{
		RealDockerPath:      "/bin/docker",
		RemoteDockerHost:    "ssh://dev",
		StateDir:            state,
		LocalBindAddress:    "127.0.0.1",
		RemoteWorkspaceRoot: remote,
		MutagenPath:         "/usr/bin/true",
	}
	for _, args := range [][]string{
		{"run", "-d", "-v", ".:/app", "-p", "3000:80", "nginx"},
		{"stop", "container-id"},
		{"start", "container-id"},
	} {
		if err := a.Run(context.Background(), Invocation{
			Entrypoint: "docker",
			Args:       args,
			Env:        map[string]string{},
			Cwd:        cwd,
			Config:     cfg,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(syncDriver.projections) != 2 {
		t.Fatalf("expected initial sync plus restored sync, got %+v", syncDriver.projections)
	}
	if tunnels.ActiveCount() != 1 {
		t.Fatalf("start should restore managed tunnel")
	}
	id := session.Identity(cwd, "ssh://dev", filepath.Base(cwd))
	loaded, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Syncs) != 1 || !loaded.Syncs[0].Active {
		t.Fatalf("sync should be active after start: %+v", loaded.Syncs)
	}
	if len(loaded.Tunnels) != 1 || !loaded.Tunnels[0].Active {
		t.Fatalf("tunnel should be active after start: %+v", loaded.Tunnels)
	}
}

func TestDockerStartFailureSuspendsRestoredManagedSession(t *testing.T) {
	cwd := t.TempDir()
	remote := t.TempDir()
	state := t.TempDir()
	exec := &RecordingExecutor{}
	tunnels := ports.NewMemoryTunnelManager()
	store := session.NewStore(state)
	syncDriver := &recordingSyncDriver{status: dbsync.Status{
		Backend:           "mutagen",
		MutagenName:       "dockbridge-sync",
		MutagenIdentifier: "sync_abc",
		RemoteEndpoint:    "dev:/remote",
		LastStatus:        "Watching for changes",
		Active:            true,
	}}
	a := App{
		Executor:        exec,
		SyncDriver:      syncDriver,
		TunnelStarter:   tunnels,
		SessionStore:    store,
		RemotePortStart: 49152,
		PortAvailable:   func(string, int) bool { return true },
	}
	cfg := config.Config{
		RealDockerPath:      "/bin/docker",
		RemoteDockerHost:    "ssh://dev",
		StateDir:            state,
		LocalBindAddress:    "127.0.0.1",
		RemoteWorkspaceRoot: remote,
		MutagenPath:         "/usr/bin/true",
	}
	for _, args := range [][]string{
		{"run", "-d", "-v", ".:/app", "-p", "3000:80", "nginx"},
		{"stop", "container-id"},
	} {
		if err := a.Run(context.Background(), Invocation{
			Entrypoint: "docker",
			Args:       args,
			Env:        map[string]string{},
			Cwd:        cwd,
			Config:     cfg,
		}); err != nil {
			t.Fatal(err)
		}
	}

	exec.Err = errors.New("docker start failed")
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"start", "container-id"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config:     cfg,
	})
	if err == nil || !strings.Contains(err.Error(), "docker start failed") {
		t.Fatalf("expected docker start failure, got %v", err)
	}
	if tunnels.ActiveCount() != 0 {
		t.Fatalf("failed start should clean restored tunnel")
	}
	id := session.Identity(cwd, "ssh://dev", filepath.Base(cwd))
	loaded, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Syncs) != 1 || loaded.Syncs[0].Active {
		t.Fatalf("failed start should leave sync inactive: %+v", loaded.Syncs)
	}
	if len(loaded.Tunnels) != 1 || loaded.Tunnels[0].Active {
		t.Fatalf("failed start should leave tunnel inactive: %+v", loaded.Tunnels)
	}
}

func TestDockerRmPurgesManagedSessionAndRemoteWorkspace(t *testing.T) {
	cwd := t.TempDir()
	remote := t.TempDir()
	state := t.TempDir()
	exec := &RecordingExecutor{}
	tunnels := ports.NewMemoryTunnelManager()
	store := session.NewStore(state)
	cleaner := &recordingRemoteCleaner{}
	a := App{
		Executor:        exec,
		SyncDriver:      dbsync.LocalMirrorDriver{},
		TunnelStarter:   tunnels,
		SessionStore:    store,
		RemoteCleaner:   cleaner,
		RemotePortStart: 49152,
		PortAvailable:   func(string, int) bool { return true },
	}
	cfg := config.Config{
		RealDockerPath:      "/bin/docker",
		RemoteDockerHost:    "ssh://dev",
		StateDir:            state,
		LocalBindAddress:    "127.0.0.1",
		RemoteWorkspaceRoot: remote,
	}
	if err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"run", "-d", "-v", ".:/app", "-p", "3000:80", "nginx"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config:     cfg,
	}); err != nil {
		t.Fatal(err)
	}
	id := session.Identity(cwd, "ssh://dev", filepath.Base(cwd))
	loaded, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"rm", "container-id"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config:     cfg,
	}); err != nil {
		t.Fatal(err)
	}
	if tunnels.ActiveCount() != 0 {
		t.Fatalf("rm should stop managed tunnel")
	}
	if len(cleaner.paths) != 1 || cleaner.paths[0] != loaded.RemoteWorkspace {
		t.Fatalf("remote workspace cleanup mismatch: %+v want %q", cleaner.paths, loaded.RemoteWorkspace)
	}
	if _, err := store.Load(id); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session should be deleted after rm, got %v", err)
	}
}

func TestDockerLifecycleWithoutSessionIsNoopAfterDockerSuccess(t *testing.T) {
	state := t.TempDir()
	exec := &RecordingExecutor{}
	cleaner := &recordingRemoteCleaner{}
	a := App{
		Executor:      exec,
		SessionStore:  session.NewStore(state),
		RemoteCleaner: cleaner,
	}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"rm", "missing-session-container"},
		Env:        map[string]string{},
		Cwd:        t.TempDir(),
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			StateDir:            state,
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: "/remote",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(exec.Calls) != 1 || strings.Join(exec.Calls[0].Args, " ") != "-H ssh://dev rm missing-session-container" {
		t.Fatalf("unexpected docker call: %+v", exec.Calls)
	}
	if len(cleaner.paths) != 0 {
		t.Fatalf("remote cleanup should be skipped without a session: %+v", cleaner.paths)
	}
}

func TestDockerbridgeSessionsListsTrackedSessions(t *testing.T) {
	state := t.TempDir()
	store := session.NewStore(state)
	if err := store.Save(session.Session{
		ID:              "sess-a",
		LocalRoot:       "/Users/me/project",
		RemoteTarget:    "ssh://dev",
		RemoteWorkspace: "/srv/dockbridge/sess-a",
		Syncs: []session.SyncState{
			{ID: "sync-1", Active: true, Backend: "mutagen"},
			{ID: "sync-2", Active: false, Backend: "mutagen"},
		},
		Tunnels: []session.TunnelState{
			{ID: "tunnel-1", LocalPort: 3000, RemotePort: 49152, Active: true},
		},
	}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	a := App{
		Executor:     &RecordingExecutor{},
		SessionStore: store,
		Output:       &out,
		ContainerTracker: recordingContainerTracker{containers: []ContainerMetadata{{
			ID:         "container-1234567890abcdef",
			Name:       "nginx",
			MountPaths: []string{"/srv/dockbridge/sess-a/Downloads"},
		}}},
	}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "dockerbridge",
		Args:       []string{"sessions"},
		Env:        map[string]string{},
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			StateDir:            state,
			RemoteWorkspaceRoot: "/srv/dockbridge",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"ID", "LOCAL ROOT", "REMOTE", "WORKSPACE", "SYNCS", "TUNNELS", "CONTAINERS", "sess-a", "1/2", "1/1", "nginx:container-12"} {
		if !strings.Contains(got, want) {
			t.Fatalf("session list missing %q in:\n%s", want, got)
		}
	}
}

func TestDockerbridgeSessionsListsContainersPerRemote(t *testing.T) {
	state := t.TempDir()
	store := session.NewStore(state)
	for _, sess := range []session.Session{
		{
			ID:              "sess-a",
			LocalRoot:       "/Users/me/project-a",
			RemoteTarget:    "ssh://dev-a",
			RemoteWorkspace: "/srv/dockbridge/sess-a",
		},
		{
			ID:              "sess-b",
			LocalRoot:       "/Users/me/project-b",
			RemoteTarget:    "ssh://dev-b",
			RemoteWorkspace: "/srv/dockbridge/sess-b",
		},
	} {
		if err := store.Save(sess); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	a := App{
		SessionStore: store,
		Output:       &out,
		ContainerTracker: remoteContainerTracker{containers: map[string][]ContainerMetadata{
			"ssh://dev-a": {{
				ID:         "container-a1234567890",
				Name:       "web-a",
				MountPaths: []string{"/srv/dockbridge/sess-a"},
			}},
			"ssh://dev-b": {{
				ID:         "container-b1234567890",
				Name:       "web-b",
				MountPaths: []string{"/srv/dockbridge/sess-b"},
			}},
		}},
	}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "dockerbridge",
		Args:       []string{"sessions"},
		Env:        map[string]string{},
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev-a",
			StateDir:            state,
			RemoteWorkspaceRoot: "/srv/dockbridge",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"web-a:container-a1", "web-b:container-b1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("session list missing %q in:\n%s", want, got)
		}
	}
}

func TestContainerSummaryKeepsSessionListCompact(t *testing.T) {
	got := containerSummary([]ContainerMetadata{
		{ID: "container-a1234567890", Name: "web"},
		{ID: "container-b1234567890", Name: "worker"},
		{ID: "container-c1234567890", Name: "db"},
		{ID: "container-d1234567890", Name: "cache"},
	})
	want := "web:container-a1,worker:container-b1,db:container-c1,+1 more"
	if got != want {
		t.Fatalf("containerSummary = %q, want %q", got, want)
	}
}

func TestDockerbridgeSessionsHelp(t *testing.T) {
	var out bytes.Buffer
	a := App{Output: &out}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "dockerbridge",
		Args:       []string{"sessions", "-h"},
		Env:        map[string]string{},
		Config:     config.Config{},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"Usage:", "dockerbridge sessions", "dockerbridge sessions cleanup"} {
		if !strings.Contains(got, want) {
			t.Fatalf("help output missing %q in:\n%s", want, got)
		}
	}
}

func TestDockerbridgeSessionsCleanupPurgesOnlySessionsWithoutContainers(t *testing.T) {
	state := t.TempDir()
	store := session.NewStore(state)
	generated := filepath.Join(t.TempDir(), "compose.yml")
	if err := os.WriteFile(generated, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(session.Session{
		ID:              "sess-a",
		LocalRoot:       "/Users/me/project-a",
		RemoteTarget:    "ssh://dev-a",
		RemoteWorkspace: "/srv/dockbridge/sess-a",
		Syncs: []session.SyncState{{
			ID:          "sync-1",
			Backend:     "mutagen",
			MutagenName: "dockbridge-sync-a",
			Active:      true,
		}},
		Tunnels: []session.TunnelState{{
			ID:         "tunnel-1",
			LocalBind:  "127.0.0.1",
			LocalPort:  3000,
			RemotePort: 49152,
			Active:     true,
		}},
		GeneratedFiles: []string{generated},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(session.Session{
		ID:              "sess-b",
		LocalRoot:       "/Users/me/project-b",
		RemoteTarget:    "ssh://dev-b",
		RemoteWorkspace: "/srv/dockbridge/sess-b",
		Syncs: []session.SyncState{{
			ID:         "sync-2",
			RemotePath: "/srv/dockbridge/sess-b/app",
			Active:     true,
		}},
		Tunnels: []session.TunnelState{{
			ID:         "tunnel-2",
			LocalBind:  "127.0.0.1",
			LocalPort:  3001,
			RemotePort: 49153,
			Active:     true,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	exec := &RecordingExecutor{}
	tunnels := ports.NewMemoryTunnelManager()
	cleaner := &recordingRemoteCleaner{}
	var out bytes.Buffer
	a := App{
		Executor:        exec,
		TunnelStarter:   tunnels,
		SessionStore:    store,
		RemoteCleaner:   cleaner,
		RemoteValidator: failingValidator{},
		ContainerTracker: recordingContainerTracker{
			containers: []ContainerMetadata{{
				ID:         "container-123",
				MountPaths: []string{"/srv/dockbridge/sess-b/app"},
			}},
		},
		Output: &out,
		MutagenTerminator: func(_ context.Context, _ string, name string) error {
			if name != "dockbridge-sync-a" {
				t.Fatalf("unexpected mutagen name %q", name)
			}
			return errors.New("sync not found")
		},
	}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "dockerbridge",
		Args:       []string{"sessions", "cleanup"},
		Env:        map[string]string{},
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			StateDir:            state,
			RemoteWorkspaceRoot: "/srv/dockbridge",
			MutagenPath:         "mutagen",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(exec.Calls) != 0 {
		t.Fatalf("native cleanup should not invoke docker: %+v", exec.Calls)
	}
	if tunnels.ActiveCount() != 0 {
		t.Fatalf("cleanup should leave no active memory tunnels")
	}
	if len(cleaner.paths) != 1 {
		t.Fatalf("remote cleanup paths = %+v", cleaner.paths)
	}
	if _, err := os.Stat(generated); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("generated file should be deleted, got %v", err)
	}
	if _, err := store.Load("sess-a"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sess-a should be deleted, got %v", err)
	}
	loaded, err := store.Load("sess-b")
	if err != nil {
		t.Fatalf("sess-b should remain, got %v", err)
	}
	if len(loaded.Syncs) != 1 || !loaded.Syncs[0].Active {
		t.Fatalf("sess-b should remain active: %+v", loaded.Syncs)
	}
	if !strings.Contains(out.String(), "Cleaned 1 DockBridge session(s)") || !strings.Contains(out.String(), "Skipped 1 active DockBridge session(s)") {
		t.Fatalf("cleanup output mismatch: %s", out.String())
	}
}

func TestDockerbridgeSessionsCleanupKeepsSessionsReferencedByPublishedPorts(t *testing.T) {
	state := t.TempDir()
	store := session.NewStore(state)
	if err := store.Save(session.Session{
		ID:              "sess-a",
		LocalRoot:       "/Users/me/project-a",
		RemoteTarget:    "ssh://dev-a",
		RemoteWorkspace: "/srv/dockbridge/sess-a",
		Tunnels: []session.TunnelState{{
			ID:         "tunnel-1",
			LocalBind:  "127.0.0.1",
			LocalPort:  3000,
			RemotePort: 49152,
			Active:     true,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	cleaner := &recordingRemoteCleaner{}
	var out bytes.Buffer
	a := App{
		SessionStore:  store,
		RemoteCleaner: cleaner,
		ContainerTracker: recordingContainerTracker{
			containers: []ContainerMetadata{{
				ID:             "container-123",
				PublishedPorts: []int{49152},
			}},
		},
		Output: &out,
	}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "dockerbridge",
		Args:       []string{"sessions", "cleanup"},
		Env:        map[string]string{},
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			StateDir:            state,
			RemoteWorkspaceRoot: "/srv/dockbridge",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleaner.paths) != 0 {
		t.Fatalf("active port-backed session should not be cleaned: %+v", cleaner.paths)
	}
	if _, err := store.Load("sess-a"); err != nil {
		t.Fatalf("sess-a should remain, got %v", err)
	}
	if !strings.Contains(out.String(), "Cleaned 0 DockBridge session(s)") || !strings.Contains(out.String(), "Skipped 1 active DockBridge session(s)") {
		t.Fatalf("cleanup output mismatch: %s", out.String())
	}
}

func TestDockerEntrypointDoesNotExposeNativeSessionsCommand(t *testing.T) {
	exec := &RecordingExecutor{}
	a := App{Executor: exec}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"sessions"},
		Env:        map[string]string{},
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			RemoteWorkspaceRoot: "/srv/dockbridge",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported docker command") {
		t.Fatalf("expected unsupported docker sessions command, got %v", err)
	}
	if len(exec.Calls) != 0 {
		t.Fatalf("unsupported docker sessions should not invoke docker: %+v", exec.Calls)
	}
}

func TestNativeSessionsRequiresSSHRemoteHost(t *testing.T) {
	state := t.TempDir()
	a := App{SessionStore: session.NewStore(state)}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "dockerbridge",
		Args:       []string{"sessions"},
		Env:        map[string]string{},
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "tcp://docker.example.com:2376",
			RemoteWorkspaceRoot: "/srv/dockbridge",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "requires an ssh:// Docker host") {
		t.Fatalf("expected non-ssh sessions error, got %v", err)
	}
}

func TestDockerRunWithNonSSHRemoteSkipsSyncAndTunnelTranslation(t *testing.T) {
	cwd := t.TempDir()
	state := t.TempDir()
	exec := &RecordingExecutor{}
	syncDriver := &recordingSyncDriver{status: dbsync.Status{Active: true}}
	tunnels := ports.NewMemoryTunnelManager()
	a := App{
		Executor:        exec,
		SyncDriver:      syncDriver,
		TunnelStarter:   tunnels,
		SessionStore:    session.NewStore(state),
		RemotePortStart: 49152,
		PortAvailable:   func(string, int) bool { return true },
	}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"run", "-v", ".:/app", "-p", "3000:80", "nginx"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "tcp://docker.example.com:2376",
			StateDir:            state,
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: "/remote",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(syncDriver.projections) != 0 {
		t.Fatalf("non-ssh run should not start syncs: %+v", syncDriver.projections)
	}
	if tunnels.ActiveCount() != 0 {
		t.Fatalf("non-ssh run should not start tunnels")
	}
	got := strings.Join(exec.Calls[0].Args, " ")
	if got != "-H tcp://docker.example.com:2376 run -v .:/app -p 3000:80 nginx" {
		t.Fatalf("args = %q", got)
	}
}

func TestInteractiveDockerRunKeepsTunnelForDetachSequence(t *testing.T) {
	cwd := t.TempDir()
	remote := t.TempDir()
	state := t.TempDir()
	exec := &RecordingExecutor{ContainerID: "container-123"}
	tunnels := ports.NewMemoryTunnelManager()
	a := App{
		Executor:           exec,
		TunnelStarter:      tunnels,
		SessionStore:       session.NewStore(state),
		ContainerInspector: recordingContainerInspector{running: true},
		RemotePortStart:    49152,
		PortAvailable:      func(string, int) bool { return true },
	}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"run", "-it", "-p", "3000:80", "nginx"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			StateDir:            state,
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: remote,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tunnels.ActiveCount() != 1 {
		t.Fatalf("interactive command should keep tunnel for docker detach sequence")
	}
}

func TestInterruptedInteractiveDockerRunCleansManagedResources(t *testing.T) {
	cwd := t.TempDir()
	state := t.TempDir()
	store := session.NewStore(state)
	ctx, cancel := context.WithCancel(context.Background())
	exec := &cancelingExecutor{cancel: cancel, err: context.Canceled}
	tunnels := ports.NewMemoryTunnelManager()
	syncDriver := &recordingSyncDriver{status: dbsync.Status{
		Backend:           "mutagen",
		MutagenName:       "dockbridge-sync",
		MutagenIdentifier: "sync_abc",
		RemoteEndpoint:    "ssh://dev/remote",
		LastStatus:        "Watching for changes",
		Active:            true,
	}}
	a := App{
		Executor:        exec,
		SyncDriver:      syncDriver,
		TunnelStarter:   tunnels,
		SessionStore:    store,
		RemotePortStart: 49152,
		PortAvailable:   func(string, int) bool { return true },
	}
	err := a.Run(ctx, Invocation{
		Entrypoint: "docker",
		Args:       []string{"run", "-it", "-v", ".:/app", "-p", "3000:80", "nginx"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			StateDir:            state,
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: "/remote",
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled run error, got %v", err)
	}
	if syncDriver.stops != 1 {
		t.Fatalf("interrupted run should stop started syncs, got %d stops", syncDriver.stops)
	}
	if syncDriver.stopContextsCanceled != 0 {
		t.Fatalf("cleanup should use a fresh non-canceled context, got %d canceled stop contexts", syncDriver.stopContextsCanceled)
	}
	if tunnels.ActiveCount() != 0 {
		t.Fatalf("interrupted interactive run should stop started tunnels")
	}
	id := session.Identity(cwd, "ssh://dev", filepath.Base(cwd))
	loaded, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Syncs) != 1 || loaded.Syncs[0].Active {
		t.Fatalf("interrupted run should persist inactive sync state: %+v", loaded.Syncs)
	}
	if len(loaded.Tunnels) != 1 || loaded.Tunnels[0].Active || loaded.Tunnels[0].PID != 0 {
		t.Fatalf("interrupted run should persist inactive tunnel state: %+v", loaded.Tunnels)
	}
}

func TestInteractiveDockerRunSuccessfulExitAfterCancellationCleansManagedResources(t *testing.T) {
	cwd := t.TempDir()
	state := t.TempDir()
	store := session.NewStore(state)
	ctx, cancel := context.WithCancel(context.Background())
	exec := &cancelingExecutor{cancel: cancel}
	tunnels := ports.NewMemoryTunnelManager()
	syncDriver := &recordingSyncDriver{status: dbsync.Status{
		Backend:           "mutagen",
		MutagenName:       "dockbridge-sync",
		MutagenIdentifier: "sync_abc",
		RemoteEndpoint:    "ssh://dev/remote",
		LastStatus:        "Watching for changes",
		Active:            true,
	}}
	a := App{
		Executor:        exec,
		SyncDriver:      syncDriver,
		TunnelStarter:   tunnels,
		SessionStore:    store,
		RemotePortStart: 49152,
		PortAvailable:   func(string, int) bool { return true },
	}
	err := a.Run(ctx, Invocation{
		Entrypoint: "docker",
		Args:       []string{"run", "-it", "-v", ".:/app", "-p", "3000:80", "nginx"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			StateDir:            state,
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: "/remote",
		},
	})
	if err != nil {
		t.Fatalf("expected successful docker exit after cancellation, got %v", err)
	}
	if syncDriver.stops != 1 {
		t.Fatalf("canceled run with successful docker exit should stop syncs, got %d stops", syncDriver.stops)
	}
	if tunnels.ActiveCount() != 0 {
		t.Fatalf("canceled run with successful docker exit should stop tunnels")
	}
	id := session.Identity(cwd, "ssh://dev", filepath.Base(cwd))
	loaded, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Syncs) != 1 || loaded.Syncs[0].Active {
		t.Fatalf("canceled run should persist inactive sync state: %+v", loaded.Syncs)
	}
	if len(loaded.Tunnels) != 1 || loaded.Tunnels[0].Active {
		t.Fatalf("canceled run should persist inactive tunnel state: %+v", loaded.Tunnels)
	}
}

func TestContextCancellationCleansInteractiveRunBeforeDockerReturns(t *testing.T) {
	cwd := t.TempDir()
	state := t.TempDir()
	store := session.NewStore(state)
	ctx, cancel := context.WithCancel(context.Background())
	exec := &blockingExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
		err:     context.Canceled,
	}
	tunnels := ports.NewMemoryTunnelManager()
	stopCh := make(chan struct{}, 1)
	syncDriver := &recordingSyncDriver{
		status: dbsync.Status{
			Backend:           "mutagen",
			MutagenName:       "dockbridge-sync",
			MutagenIdentifier: "sync_abc",
			RemoteEndpoint:    "ssh://dev/remote",
			LastStatus:        "Watching for changes",
			Active:            true,
		},
		stopCh: stopCh,
	}
	a := App{
		Executor:        exec,
		SyncDriver:      syncDriver,
		TunnelStarter:   tunnels,
		SessionStore:    store,
		RemotePortStart: 49152,
		PortAvailable:   func(string, int) bool { return true },
	}
	done := make(chan error, 1)
	go func() {
		done <- a.Run(ctx, Invocation{
			Entrypoint: "docker",
			Args:       []string{"run", "-it", "-v", ".:/app", "-p", "3000:80", "nginx"},
			Env:        map[string]string{},
			Cwd:        cwd,
			Config: config.Config{
				RealDockerPath:      "/bin/docker",
				RemoteDockerHost:    "ssh://dev",
				StateDir:            state,
				LocalBindAddress:    "127.0.0.1",
				RemoteWorkspaceRoot: "/remote",
			},
		})
	}()
	<-exec.started
	cancel()
	select {
	case <-stopCh:
	case <-time.After(time.Second):
		t.Fatal("context cancellation should clean resources before docker returns")
	}
	if tunnels.ActiveCount() != 0 {
		t.Fatalf("context cancellation should stop tunnels before docker returns")
	}
	close(exec.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled run error, got %v", err)
	}
	loaded, err := store.Load(session.Identity(cwd, "ssh://dev", filepath.Base(cwd)))
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Syncs) != 1 || loaded.Syncs[0].Active {
		t.Fatalf("context cancellation should persist inactive sync state: %+v", loaded.Syncs)
	}
	if len(loaded.Tunnels) != 1 || loaded.Tunnels[0].Active {
		t.Fatalf("context cancellation should persist inactive tunnel state: %+v", loaded.Tunnels)
	}
}

func TestInteractiveDockerRunSuccessfulExitCleansWhenContainerStopped(t *testing.T) {
	cwd := t.TempDir()
	state := t.TempDir()
	store := session.NewStore(state)
	exec := &RecordingExecutor{ContainerID: "container-123"}
	tunnels := ports.NewMemoryTunnelManager()
	syncDriver := &recordingSyncDriver{status: dbsync.Status{
		Backend:           "mutagen",
		MutagenName:       "dockbridge-sync",
		MutagenIdentifier: "sync_abc",
		RemoteEndpoint:    "ssh://dev/remote",
		LastStatus:        "Watching for changes",
		Active:            true,
	}}
	a := App{
		Executor:           exec,
		SyncDriver:         syncDriver,
		TunnelStarter:      tunnels,
		SessionStore:       store,
		ContainerInspector: recordingContainerInspector{running: false},
		RemotePortStart:    49152,
		PortAvailable:      func(string, int) bool { return true },
	}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"run", "-it", "-v", ".:/app", "-p", "3000:80", "nginx"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			StateDir:            state,
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: "/remote",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !callHasCIDFile(exec.Calls[0]) {
		t.Fatalf("interactive run should inject --cidfile: %+v", exec.Calls[0].Args)
	}
	cidfile, _ := callCIDFile(exec.Calls[0])
	if _, err := os.Stat(cidfile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("internal cidfile should be removed after lifecycle evaluation, stat err: %v", err)
	}
	if syncDriver.stops != 1 {
		t.Fatalf("stopped interactive container should stop syncs, got %d stops", syncDriver.stops)
	}
	if tunnels.ActiveCount() != 0 {
		t.Fatalf("stopped interactive container should stop tunnels")
	}
	loaded, err := store.Load(session.Identity(cwd, "ssh://dev", filepath.Base(cwd)))
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Syncs) != 1 || loaded.Syncs[0].Active {
		t.Fatalf("stopped interactive container should persist inactive sync state: %+v", loaded.Syncs)
	}
	if len(loaded.Tunnels) != 1 || loaded.Tunnels[0].Active {
		t.Fatalf("stopped interactive container should persist inactive tunnel state: %+v", loaded.Tunnels)
	}
}

func TestInteractiveDockerRunSuccessfulExitKeepsResourcesWhenContainerRunning(t *testing.T) {
	cwd := t.TempDir()
	state := t.TempDir()
	store := session.NewStore(state)
	exec := &RecordingExecutor{ContainerID: "container-123"}
	tunnels := ports.NewMemoryTunnelManager()
	syncDriver := &recordingSyncDriver{status: dbsync.Status{
		Backend:           "mutagen",
		MutagenName:       "dockbridge-sync",
		MutagenIdentifier: "sync_abc",
		RemoteEndpoint:    "ssh://dev/remote",
		LastStatus:        "Watching for changes",
		Active:            true,
	}}
	a := App{
		Executor:           exec,
		SyncDriver:         syncDriver,
		TunnelStarter:      tunnels,
		SessionStore:       store,
		ContainerInspector: recordingContainerInspector{running: true},
		RemotePortStart:    49152,
		PortAvailable:      func(string, int) bool { return true },
	}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"run", "-it", "-v", ".:/app", "-p", "3000:80", "nginx"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			StateDir:            state,
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: "/remote",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if syncDriver.stops != 0 {
		t.Fatalf("running detached container should keep syncs, got %d stops", syncDriver.stops)
	}
	if tunnels.ActiveCount() != 1 {
		t.Fatalf("running detached container should keep tunnels")
	}
	loaded, err := store.Load(session.Identity(cwd, "ssh://dev", filepath.Base(cwd)))
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Syncs) != 1 || !loaded.Syncs[0].Active {
		t.Fatalf("running detached container should persist active sync state: %+v", loaded.Syncs)
	}
	if len(loaded.Tunnels) != 1 || !loaded.Tunnels[0].Active {
		t.Fatalf("running detached container should persist active tunnel state: %+v", loaded.Tunnels)
	}
}

func TestInteractiveDockerRunWithRemovedContainerCleansResources(t *testing.T) {
	cwd := t.TempDir()
	remote := t.TempDir()
	state := t.TempDir()
	userCIDFile := filepath.Join(t.TempDir(), "container.cid")
	if err := os.WriteFile(userCIDFile, []byte("container-123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := session.NewStore(state)
	exec := &RecordingExecutor{}
	tunnels := ports.NewMemoryTunnelManager()
	cleaner := &recordingRemoteCleaner{}
	syncDriver := &recordingSyncDriver{status: dbsync.Status{
		Backend:           "mutagen",
		MutagenName:       "dockbridge-sync",
		MutagenIdentifier: "sync_abc",
		RemoteEndpoint:    "ssh://dev/remote",
		LastStatus:        "Watching for changes",
		Active:            true,
	}}
	a := App{
		Executor:           exec,
		SyncDriver:         syncDriver,
		TunnelStarter:      tunnels,
		SessionStore:       store,
		RemoteCleaner:      cleaner,
		ContainerInspector: recordingContainerInspector{err: os.ErrNotExist},
		RemotePortStart:    49152,
		PortAvailable:      func(string, int) bool { return true },
	}
	id := session.Identity(cwd, "ssh://dev", filepath.Base(cwd))
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"run", "--rm", "-it", "--cidfile", userCIDFile, "-v", ".:/app", "-p", "3000:80", "nginx"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			StateDir:            state,
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: remote,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if syncDriver.stops != 1 {
		t.Fatalf("removed interactive container should stop syncs, got %d stops", syncDriver.stops)
	}
	if tunnels.ActiveCount() != 0 {
		t.Fatalf("removed interactive container should stop tunnels")
	}
	if _, err := os.Stat(userCIDFile); err != nil {
		t.Fatalf("user cidfile should be preserved: %v", err)
	}
	if len(cleaner.paths) != 1 || cleaner.paths[0] != session.WorkspacePath(remote, id) {
		t.Fatalf("remote workspace cleanup mismatch: %+v", cleaner.paths)
	}
	if _, err := store.Load(id); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("auto-remove run should delete session, got %v", err)
	}
}

func TestDockerStopPurgesAutoRemoveManagedSession(t *testing.T) {
	cwd := t.TempDir()
	remote := t.TempDir()
	state := t.TempDir()
	exec := &RecordingExecutor{}
	tunnels := ports.NewMemoryTunnelManager()
	store := session.NewStore(state)
	cleaner := &recordingRemoteCleaner{}
	a := App{
		Executor:        exec,
		SyncDriver:      dbsync.LocalMirrorDriver{},
		TunnelStarter:   tunnels,
		SessionStore:    store,
		RemoteCleaner:   cleaner,
		RemotePortStart: 49152,
		PortAvailable:   func(string, int) bool { return true },
	}
	cfg := config.Config{
		RealDockerPath:      "/bin/docker",
		RemoteDockerHost:    "ssh://dev",
		StateDir:            state,
		LocalBindAddress:    "127.0.0.1",
		RemoteWorkspaceRoot: remote,
	}
	if err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"run", "-d", "--rm", "-v", ".:/app", "-p", "3000:80", "nginx"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config:     cfg,
	}); err != nil {
		t.Fatal(err)
	}
	id := session.Identity(cwd, "ssh://dev", filepath.Base(cwd))
	loaded, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"stop", "container-id"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config:     cfg,
	}); err != nil {
		t.Fatal(err)
	}
	if tunnels.ActiveCount() != 0 {
		t.Fatalf("stop should stop managed tunnel")
	}
	if len(cleaner.paths) != 1 || cleaner.paths[0] != loaded.RemoteWorkspace {
		t.Fatalf("remote workspace cleanup mismatch: %+v want %q", cleaner.paths, loaded.RemoteWorkspace)
	}
	if _, err := store.Load(id); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("auto-remove session should be deleted after stop, got %v", err)
	}
}

func TestInteractiveDockerRunCleanupToleratesMissingMutagenSession(t *testing.T) {
	cwd := t.TempDir()
	state := t.TempDir()
	store := session.NewStore(state)
	exec := &RecordingExecutor{ContainerID: "container-123"}
	tunnels := ports.NewMemoryTunnelManager()
	syncDriver := &recordingSyncDriver{
		status: dbsync.Status{
			Backend:           "mutagen",
			MutagenName:       "dockbridge-sync",
			MutagenIdentifier: "sync_abc",
			RemoteEndpoint:    "ssh://dev/remote",
			LastStatus:        "Watching for changes",
			Active:            true,
		},
		stopErr: errors.New("unknown synchronization session"),
	}
	a := App{
		Executor:           exec,
		SyncDriver:         syncDriver,
		TunnelStarter:      tunnels,
		SessionStore:       store,
		ContainerInspector: recordingContainerInspector{running: false},
		RemotePortStart:    49152,
		PortAvailable:      func(string, int) bool { return true },
	}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"run", "-it", "-v", ".:/app", "-p", "3000:80", "nginx"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			StateDir:            state,
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: "/remote",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(session.Identity(cwd, "ssh://dev", filepath.Base(cwd)))
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Syncs) != 1 || loaded.Syncs[0].Active {
		t.Fatalf("missing Mutagen session cleanup should still persist inactive sync state: %+v", loaded.Syncs)
	}
}

func TestDockerRunPersistsMutagenSyncStateAndSkipsNamedVolumes(t *testing.T) {
	cwd := t.TempDir()
	state := t.TempDir()
	store := session.NewStore(state)
	syncDriver := &recordingSyncDriver{status: dbsync.Status{
		Backend:           "mutagen",
		MutagenName:       "dockbridge-sync",
		MutagenIdentifier: "sync_abc",
		RemoteEndpoint:    "ssh://dev/remote",
		LastStatus:        "Watching for changes",
		Active:            true,
	}}
	exec := &RecordingExecutor{}
	a := App{
		Executor:      exec,
		SyncDriver:    syncDriver,
		TunnelStarter: ports.NewMemoryTunnelManager(),
		SessionStore:  store,
		PortAvailable: func(string, int) bool { return true },
	}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"run", "-v", ".:/app", "-v", "named-data:/data", "-p", "3000:80", "nginx"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			StateDir:            state,
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: "/remote",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(syncDriver.projections) != 1 {
		t.Fatalf("expected only bind mount to sync, got %+v", syncDriver.projections)
	}
	id := session.Identity(cwd, "ssh://dev", filepath.Base(cwd))
	loaded, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Syncs) != 1 || loaded.Syncs[0].Backend != "mutagen" || loaded.Syncs[0].MutagenName != "dockbridge-sync" {
		t.Fatalf("mutagen sync state not persisted: %+v", loaded.Syncs)
	}
}

func TestDockerRunFailureCleansStartedSyncAndTunnel(t *testing.T) {
	cwd := t.TempDir()
	state := t.TempDir()
	store := session.NewStore(state)
	syncDriver := &recordingSyncDriver{status: dbsync.Status{
		Backend:           "mutagen",
		MutagenName:       "dockbridge-sync",
		MutagenIdentifier: "sync_abc",
		RemoteEndpoint:    "ssh://dev/remote",
		LastStatus:        "Watching for changes",
		Active:            true,
	}}
	exec := &RecordingExecutor{Err: errors.New("container start failed")}
	tunnels := ports.NewMemoryTunnelManager()
	a := App{
		Executor:        exec,
		SyncDriver:      syncDriver,
		TunnelStarter:   tunnels,
		SessionStore:    store,
		RemotePortStart: 49152,
		PortAvailable:   func(string, int) bool { return true },
	}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"run", "-d", "-v", ".:/app", "-p", "3000:80", "nginx"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			StateDir:            state,
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: "/remote",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "container start failed") {
		t.Fatalf("expected container start failure, got %v", err)
	}
	if syncDriver.stops != 1 {
		t.Fatalf("failed run should stop started syncs, got %d stops", syncDriver.stops)
	}
	if tunnels.ActiveCount() != 0 {
		t.Fatalf("failed run should stop started tunnels")
	}
	id := session.Identity(cwd, "ssh://dev", filepath.Base(cwd))
	loaded, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Syncs) != 1 || loaded.Syncs[0].Active {
		t.Fatalf("failed run should persist inactive sync state: %+v", loaded.Syncs)
	}
	if len(loaded.Tunnels) != 1 || loaded.Tunnels[0].Active || loaded.Tunnels[0].PID != 0 {
		t.Fatalf("failed run should persist inactive tunnel state: %+v", loaded.Tunnels)
	}
}

func TestComposeUpGeneratesTranslatedComposeAndInvokesRemoteDocker(t *testing.T) {
	cwd := t.TempDir()
	state := t.TempDir()
	remote := t.TempDir()
	composeFile := filepath.Join(cwd, "compose.yml")
	if err := os.WriteFile(composeFile, []byte("services:\n  web:\n    build: .\n    volumes:\n      - .:/app\n    ports:\n      - \"8080:80\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exec := &RecordingExecutor{}
	a := App{
		Executor:        exec,
		SyncDriver:      dbsync.LocalMirrorDriver{},
		TunnelStarter:   ports.NewMemoryTunnelManager(),
		SessionStore:    session.NewStore(state),
		RemotePortStart: 49152,
		PortAvailable:   func(string, int) bool { return true },
	}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"compose", "up"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			StateDir:            state,
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: remote,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(exec.Calls[0].Args, " ")
	if !strings.Contains(got, "-H ssh://dev compose -f") || !strings.Contains(got, " up") {
		t.Fatalf("compose invocation mismatch: %s", got)
	}
	after, err := os.ReadFile(composeFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), remote) {
		t.Fatalf("source compose should remain unchanged")
	}
}

func TestComposeUpFailureCleansStartedSyncAndTunnel(t *testing.T) {
	cwd := t.TempDir()
	state := t.TempDir()
	composeFile := filepath.Join(cwd, "compose.yml")
	if err := os.WriteFile(composeFile, []byte("services:\n  web:\n    volumes:\n      - .:/app\n    ports:\n      - \"8080:80\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exec := &RecordingExecutor{Err: errors.New("compose up failed")}
	tunnels := ports.NewMemoryTunnelManager()
	store := session.NewStore(state)
	syncDriver := &recordingSyncDriver{status: dbsync.Status{
		Backend:           "mutagen",
		MutagenName:       "dockbridge-sync",
		MutagenIdentifier: "sync_abc",
		RemoteEndpoint:    "ssh://dev/remote",
		LastStatus:        "Watching for changes",
		Active:            true,
	}}
	a := App{
		Executor:        exec,
		SyncDriver:      syncDriver,
		TunnelStarter:   tunnels,
		SessionStore:    store,
		RemotePortStart: 49152,
		PortAvailable:   func(string, int) bool { return true },
	}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"compose", "up", "-d"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			StateDir:            state,
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: "/remote",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "compose up failed") {
		t.Fatalf("expected compose up failure, got %v", err)
	}
	if syncDriver.stops != 1 {
		t.Fatalf("failed compose up should stop started syncs, got %d stops", syncDriver.stops)
	}
	if tunnels.ActiveCount() != 0 {
		t.Fatalf("failed compose up should stop started tunnels")
	}
	id := session.Identity(cwd, "ssh://dev", filepath.Base(cwd))
	loaded, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Syncs) != 1 || loaded.Syncs[0].Active {
		t.Fatalf("failed compose up should persist inactive sync state: %+v", loaded.Syncs)
	}
	if len(loaded.Tunnels) != 1 || loaded.Tunnels[0].Active {
		t.Fatalf("failed compose up should persist inactive tunnel state: %+v", loaded.Tunnels)
	}
}

func TestInterruptedComposeUpCleansManagedResources(t *testing.T) {
	cwd := t.TempDir()
	state := t.TempDir()
	composeFile := filepath.Join(cwd, "compose.yml")
	if err := os.WriteFile(composeFile, []byte("services:\n  web:\n    volumes:\n      - .:/app\n    ports:\n      - \"8080:80\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	exec := &cancelingExecutor{cancel: cancel, err: context.Canceled}
	tunnels := ports.NewMemoryTunnelManager()
	store := session.NewStore(state)
	syncDriver := &recordingSyncDriver{status: dbsync.Status{
		Backend:           "mutagen",
		MutagenName:       "dockbridge-sync",
		MutagenIdentifier: "sync_abc",
		RemoteEndpoint:    "ssh://dev/remote",
		LastStatus:        "Watching for changes",
		Active:            true,
	}}
	a := App{
		Executor:        exec,
		SyncDriver:      syncDriver,
		TunnelStarter:   tunnels,
		SessionStore:    store,
		RemotePortStart: 49152,
		PortAvailable:   func(string, int) bool { return true },
	}
	err := a.Run(ctx, Invocation{
		Entrypoint: "docker",
		Args:       []string{"compose", "up"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			StateDir:            state,
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: "/remote",
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled compose up error, got %v", err)
	}
	if syncDriver.stops != 1 {
		t.Fatalf("interrupted compose up should stop started syncs, got %d stops", syncDriver.stops)
	}
	if syncDriver.stopContextsCanceled != 0 {
		t.Fatalf("cleanup should use a fresh non-canceled context, got %d canceled stop contexts", syncDriver.stopContextsCanceled)
	}
	if tunnels.ActiveCount() != 0 {
		t.Fatalf("interrupted compose up should stop started tunnels")
	}
	id := session.Identity(cwd, "ssh://dev", filepath.Base(cwd))
	loaded, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Syncs) != 1 || loaded.Syncs[0].Active {
		t.Fatalf("interrupted compose up should persist inactive sync state: %+v", loaded.Syncs)
	}
	if len(loaded.Tunnels) != 1 || loaded.Tunnels[0].Active {
		t.Fatalf("interrupted compose up should persist inactive tunnel state: %+v", loaded.Tunnels)
	}
}

func TestComposeLifecycleUsesGeneratedFileAndSessionActions(t *testing.T) {
	cwd := t.TempDir()
	state := t.TempDir()
	remote := t.TempDir()
	composeFile := filepath.Join(cwd, "compose.yml")
	if err := os.WriteFile(composeFile, []byte("services:\n  web:\n    volumes:\n      - .:/app\n    ports:\n      - \"8080:80\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exec := &RecordingExecutor{}
	tunnels := ports.NewMemoryTunnelManager()
	store := session.NewStore(state)
	cleaner := &recordingRemoteCleaner{}
	a := App{
		Executor:        exec,
		SyncDriver:      dbsync.LocalMirrorDriver{},
		TunnelStarter:   tunnels,
		SessionStore:    store,
		RemoteCleaner:   cleaner,
		RemotePortStart: 49152,
		PortAvailable:   func(string, int) bool { return true },
	}
	cfg := config.Config{
		RealDockerPath:      "/bin/docker",
		RemoteDockerHost:    "ssh://dev",
		StateDir:            state,
		LocalBindAddress:    "127.0.0.1",
		RemoteWorkspaceRoot: remote,
	}
	if err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"compose", "up", "-d"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config:     cfg,
	}); err != nil {
		t.Fatal(err)
	}
	id := session.Identity(cwd, "ssh://dev", filepath.Base(cwd))
	loaded, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.GeneratedFiles) != 1 {
		t.Fatalf("expected generated compose file state: %+v", loaded)
	}
	generated := loaded.GeneratedFiles[0]
	for _, step := range []struct {
		args   []string
		active bool
	}{
		{args: []string{"compose", "stop"}, active: false},
		{args: []string{"compose", "start"}, active: true},
	} {
		if err := a.Run(context.Background(), Invocation{
			Entrypoint: "docker",
			Args:       step.args,
			Env:        map[string]string{},
			Cwd:        cwd,
			Config:     cfg,
		}); err != nil {
			t.Fatal(err)
		}
		got := strings.Join(exec.Calls[len(exec.Calls)-1].Args, " ")
		if !strings.Contains(got, "compose -f "+generated) || !strings.Contains(got, " "+step.args[1]) {
			t.Fatalf("compose lifecycle did not use generated file: %s", got)
		}
		loaded, err = store.Load(id)
		if err != nil {
			t.Fatal(err)
		}
		if len(loaded.Tunnels) != 1 || loaded.Tunnels[0].Active != step.active {
			t.Fatalf("tunnel active=%v after %v: %+v", step.active, step.args, loaded.Tunnels)
		}
	}
	if err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"compose", "down"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config:     cfg,
	}); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(exec.Calls[len(exec.Calls)-1].Args, " ")
	if !strings.Contains(got, "compose -f "+generated) || !strings.Contains(got, " down") {
		t.Fatalf("compose down did not use generated file: %s", got)
	}
	if _, err := os.Stat(generated); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("generated file should be removed after down, got %v", err)
	}
	if len(cleaner.paths) != 1 || cleaner.paths[0] != loaded.RemoteWorkspace {
		t.Fatalf("compose down remote cleanup mismatch: %+v", cleaner.paths)
	}
	if _, err := store.Load(id); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session should be deleted after compose down, got %v", err)
	}
}

func TestRemoteValidatorRunsBeforeDocker(t *testing.T) {
	exec := &RecordingExecutor{}
	validatorExec := &RecordingExecutor{}
	a := App{Executor: exec, RemoteValidator: DockerValidator{Executor: validatorExec}}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"ps"},
		Env:        map[string]string{},
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: "/srv/dockbridge",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(validatorExec.Calls) != 1 {
		t.Fatalf("expected validator call")
	}
	if len(exec.Calls) != 1 {
		t.Fatalf("expected docker call")
	}
}

func TestDockerbridgeComposeFallsBackWhenNoComposeFileExists(t *testing.T) {
	exec := &RecordingExecutor{}
	a := App{Executor: exec}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "dockerbridge",
		Args:       []string{"compose", "ps"},
		Env:        map[string]string{},
		Cwd:        t.TempDir(),
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: "/srv/dockbridge",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(exec.Calls[0].Args, " ")
	if got != "-H ssh://dev compose ps" {
		t.Fatalf("dockerbridge compose fallback args = %q", got)
	}
}

func TestDockerBuildProjectsContext(t *testing.T) {
	cwd := t.TempDir()
	state := t.TempDir()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exec := &RecordingExecutor{}
	a := App{
		Executor:        exec,
		SyncDriver:      dbsync.LocalMirrorDriver{},
		SessionStore:    session.NewStore(state),
		RemotePortStart: 49152,
	}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"build", "-f", "Dockerfile", "."},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			StateDir:            state,
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: remote,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(exec.Calls[0].Args, " ")
	if !strings.Contains(got, "build -f Dockerfile") || !strings.Contains(got, "build-context") {
		t.Fatalf("build args did not project context: %s", got)
	}
}

func TestComposePortConflictFailsBeforeDocker(t *testing.T) {
	cwd := t.TempDir()
	state := t.TempDir()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "compose.yml"), []byte("services:\n  web:\n    ports:\n      - \"8080:80\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exec := &RecordingExecutor{}
	a := App{
		Executor:        exec,
		TunnelStarter:   ports.NewMemoryTunnelManager(),
		SessionStore:    session.NewStore(state),
		RemotePortStart: 49152,
		PortAvailable:   func(string, int) bool { return false },
	}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"compose", "up"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			StateDir:            state,
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: remote,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "local port conflict") {
		t.Fatalf("expected local port conflict, got %v", err)
	}
	if len(exec.Calls) != 0 {
		t.Fatalf("docker should not be called on port conflict")
	}
}

func TestRemoteValidatorFailureStopsCommand(t *testing.T) {
	exec := &RecordingExecutor{}
	a := App{Executor: exec, RemoteValidator: failingValidator{}}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"ps"},
		Env:        map[string]string{},
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: "/srv/dockbridge",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "remote Docker connectivity failed") {
		t.Fatalf("expected validator failure, got %v", err)
	}
	if len(exec.Calls) != 0 {
		t.Fatalf("docker should not run after validator failure")
	}
}

func TestSyncFailureStopsBeforeDocker(t *testing.T) {
	exec := &RecordingExecutor{}
	a := App{Executor: exec, SyncDriver: failingSyncDriver{}}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"run", "-v", ".:/app", "nginx"},
		Env:        map[string]string{},
		Cwd:        t.TempDir(),
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: "/remote",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "sync failed") {
		t.Fatalf("expected sync failure, got %v", err)
	}
	if len(exec.Calls) != 0 {
		t.Fatalf("docker should not be invoked after sync failure: %+v", exec.Calls)
	}
}

func TestDockerRunChecksLocalMountAccessBeforeSync(t *testing.T) {
	exec := &RecordingExecutor{}
	syncDriver := &recordingSyncDriver{}
	a := App{
		Executor:   exec,
		SyncDriver: syncDriver,
		LocalPathChecker: func(path string) error {
			if strings.Contains(path, "Downloads") {
				return &os.PathError{Op: "open", Path: path, Err: syscall.EPERM}
			}
			return nil
		},
	}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"run", "-v", "/Users/monlor/Downloads:/tmp", "nginx"},
		Env:        map[string]string{},
		Cwd:        t.TempDir(),
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: "/remote",
		},
	})
	if err == nil {
		t.Fatal("expected local mount access error")
	}
	for _, want := range []string{"/Users/monlor/Downloads", "macOS privacy", "Full Disk Access", "mutagen daemon stop"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to contain %q, got %v", want, err)
		}
	}
	if len(syncDriver.projections) != 0 {
		t.Fatalf("sync should not start after local access failure: %+v", syncDriver.projections)
	}
	if len(exec.Calls) != 0 {
		t.Fatalf("docker should not run after local access failure: %+v", exec.Calls)
	}
}

func TestComposeUpChecksLocalMountAccessBeforeSync(t *testing.T) {
	cwd := t.TempDir()
	state := t.TempDir()
	composeFile := filepath.Join(cwd, "compose.yml")
	if err := os.WriteFile(composeFile, []byte("services:\n  web:\n    image: nginx\n    volumes:\n      - /Users/monlor/Downloads:/data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exec := &RecordingExecutor{}
	syncDriver := &recordingSyncDriver{}
	a := App{
		Executor:     exec,
		SyncDriver:   syncDriver,
		SessionStore: session.NewStore(state),
		LocalPathChecker: func(path string) error {
			if strings.Contains(path, "Downloads") {
				return &os.PathError{Op: "open", Path: path, Err: syscall.EPERM}
			}
			return nil
		},
	}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"compose", "up"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			StateDir:            state,
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: "/remote",
		},
	})
	if err == nil {
		t.Fatal("expected local mount access error")
	}
	for _, want := range []string{"/Users/monlor/Downloads", "macOS privacy", "Full Disk Access", "mutagen daemon stop"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to contain %q, got %v", want, err)
		}
	}
	if len(syncDriver.projections) != 0 {
		t.Fatalf("sync should not start after local access failure: %+v", syncDriver.projections)
	}
	if len(exec.Calls) != 0 {
		t.Fatalf("docker should not run after local access failure: %+v", exec.Calls)
	}
}

func TestStartSyncsCleansStartedSyncWhenLaterLocalAccessFails(t *testing.T) {
	syncDriver := &recordingSyncDriver{status: dbsync.Status{Active: true}}
	checks := 0
	a := App{
		SyncDriver: syncDriver,
		LocalPathChecker: func(path string) error {
			checks++
			if checks == 2 {
				return &os.PathError{Op: "open", Path: path, Err: syscall.EPERM}
			}
			return nil
		},
	}
	_, err := a.startSyncs(context.Background(), "abc", config.Config{RemoteDockerHost: "ssh://dev"}, map[string]string{
		"/allowed": "/remote/allowed",
		"/blocked": "/remote/blocked",
	})
	if err == nil {
		t.Fatal("expected local mount access error")
	}
	if len(syncDriver.projections) != 1 {
		t.Fatalf("expected one sync to start before later access failure, got %+v", syncDriver.projections)
	}
	if syncDriver.stops != 1 {
		t.Fatalf("expected started sync to be stopped, got %d", syncDriver.stops)
	}
}

func TestOSExecutorAndEnvMap(t *testing.T) {
	if err := (OSExecutor{}).Run(context.Background(), Call{Name: "/bin/echo", Args: []string{"ok"}}); err != nil {
		t.Fatal(err)
	}
	if len(EnvMap()) == 0 {
		t.Fatal("EnvMap should expose current environment")
	}
}

func TestDockerRunInteractiveUsesSharedTerminalProcessGroup(t *testing.T) {
	cwd := t.TempDir()
	state := t.TempDir()
	exec := &RecordingExecutor{}
	cleaner := &recordingRemoteCleaner{}
	a := App{
		Executor:        exec,
		TunnelStarter:   ports.NewMemoryTunnelManager(),
		SessionStore:    session.NewStore(state),
		RemoteCleaner:   cleaner,
		RemotePortStart: 49152,
		PortAvailable:   func(string, int) bool { return true },
	}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"run", "-it", "--rm", "-p", "8080:80", "nginx"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			StateDir:            state,
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: "/remote",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(exec.Calls) != 1 || !exec.Calls[0].SharedTerminalProcessPG {
		t.Fatalf("interactive run should share the terminal process group: %+v", exec.Calls)
	}
}

func TestDockerRunNonInteractiveUsesManagedProcessGroup(t *testing.T) {
	cwd := t.TempDir()
	state := t.TempDir()
	exec := &RecordingExecutor{}
	cleaner := &recordingRemoteCleaner{}
	a := App{
		Executor:        exec,
		TunnelStarter:   ports.NewMemoryTunnelManager(),
		SessionStore:    session.NewStore(state),
		RemoteCleaner:   cleaner,
		RemotePortStart: 49152,
		PortAvailable:   func(string, int) bool { return true },
	}
	err := a.Run(context.Background(), Invocation{
		Entrypoint: "docker",
		Args:       []string{"run", "--rm", "-p", "8080:80", "nginx"},
		Env:        map[string]string{},
		Cwd:        cwd,
		Config: config.Config{
			RealDockerPath:      "/bin/docker",
			RemoteDockerHost:    "ssh://dev",
			StateDir:            state,
			LocalBindAddress:    "127.0.0.1",
			RemoteWorkspaceRoot: "/remote",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(exec.Calls) != 1 || exec.Calls[0].SharedTerminalProcessPG {
		t.Fatalf("non-interactive run should use a managed process group: %+v", exec.Calls)
	}
}

func TestDockerRunLifecycleHelpers(t *testing.T) {
	if !isInteractiveDockerRun([]string{"run", "-it", "nginx"}) {
		t.Fatal("-it should be interactive")
	}
	if !isInteractiveDockerRun([]string{"run", "--interactive", "--tty", "nginx"}) {
		t.Fatal("--interactive --tty should be interactive")
	}
	if isInteractiveDockerRun([]string{"run", "-d", "nginx"}) {
		t.Fatal("detached run should not be interactive")
	}
	if !shouldCleanupAfterDockerCommand([]string{"stop", "abc"}) || !shouldCleanupAfterDockerCommand([]string{"rm", "abc"}) {
		t.Fatal("stop/rm should trigger cleanup")
	}
	if shouldCleanupAfterDockerCommand([]string{"ps"}) || shouldCleanupAfterDockerCommand(nil) {
		t.Fatal("non-lifecycle commands should not trigger cleanup")
	}
	if !shouldRestoreBeforeDockerCommand([]string{"start", "abc"}) {
		t.Fatal("start should trigger restore")
	}
	if composeLifecycleAction([]string{"-f", "compose.yml", "down"}) != lifecyclePurge {
		t.Fatal("compose down with file flag should trigger purge")
	}
}

func TestValidateRemoteWorkspacePathRejectsUnsafeTargets(t *testing.T) {
	if err := validateRemoteWorkspacePath("/srv/dockbridge", "/srv/dockbridge/session-a"); err != nil {
		t.Fatalf("expected safe path, got %v", err)
	}
	for _, tc := range []struct {
		root      string
		workspace string
	}{
		{root: "", workspace: "/srv/dockbridge/session-a"},
		{root: "/", workspace: "/srv/dockbridge/session-a"},
		{root: "/srv/dockbridge", workspace: "/srv/dockbridge"},
		{root: "/srv/dockbridge", workspace: "/srv/dockbridge-other/session-a"},
		{root: "/srv/dockbridge", workspace: "relative/session-a"},
	} {
		if err := validateRemoteWorkspacePath(tc.root, tc.workspace); err == nil {
			t.Fatalf("expected unsafe cleanup path to fail: %+v", tc)
		}
	}
}

func TestSSHRemoteWorkspaceCleanerUsesSafeSSHCommand(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "ssh-args")
	sshPath := filepath.Join(dir, "ssh")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$SSH_ARGS_FILE\"\n"
	if err := os.WriteFile(sshPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SSH_ARGS_FILE", argsFile)

	err := (SSHRemoteWorkspaceCleaner{}).Remove(context.Background(), config.Config{
		RemoteDockerHost:    "ssh://me@example.com:2222",
		RemoteWorkspaceRoot: "/srv/dockbridge",
	}, "/srv/dockbridge/session-a")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"-p\n2222\n", "me@example.com\n", "rm -rf '/srv/dockbridge/session-a'\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("ssh args missing %q in:\n%s", want, got)
		}
	}
	for _, unexpected := range []string{"rm -rf --", "sh\n-lc", "\"$1\""} {
		if strings.Contains(got, unexpected) {
			t.Fatalf("ssh args should avoid %q in:\n%s", unexpected, got)
		}
	}
}

func TestSSHRemoteWorkspaceCleanerQuotesRemoteWorkspacePath(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "ssh-args")
	sshPath := filepath.Join(dir, "ssh")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$SSH_ARGS_FILE\"\n"
	if err := os.WriteFile(sshPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SSH_ARGS_FILE", argsFile)

	err := (SSHRemoteWorkspaceCleaner{}).Remove(context.Background(), config.Config{
		RemoteDockerHost:    "ssh://me@example.com:2222",
		RemoteWorkspaceRoot: "/srv/dockbridge",
	}, "/srv/dockbridge/session a'b")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	want := "rm -rf '/srv/dockbridge/session a'\"'\"'b'\n"
	if !strings.Contains(got, want) {
		t.Fatalf("ssh args missing quoted command %q in:\n%s", want, got)
	}
}

func TestSSHRemoteWorkspaceCleanerRejectsUnsafeBeforeSSH(t *testing.T) {
	err := (SSHRemoteWorkspaceCleaner{}).Remove(context.Background(), config.Config{
		RemoteDockerHost:    "ssh://me@example.com",
		RemoteWorkspaceRoot: "/",
	}, "/srv/dockbridge/session-a")
	if err == nil {
		t.Fatal("expected unsafe remote workspace cleanup error")
	}
}

func TestSSHTargetParsesSupportedForms(t *testing.T) {
	target, port, err := sshTarget("ssh://me@example.com:2222")
	if err != nil {
		t.Fatal(err)
	}
	if target != "me@example.com" || port != "2222" {
		t.Fatalf("ssh url target=%q port=%q", target, port)
	}
	target, port, err = sshTarget("me@example.com:1023")
	if err != nil {
		t.Fatal(err)
	}
	if target != "me@example.com" || port != "1023" {
		t.Fatalf("scp-like target=%q port=%q", target, port)
	}
	if _, _, err := sshTarget("ssh://:2222"); err == nil {
		t.Fatal("expected invalid ssh target error")
	}
}

type failingValidator struct{}

func (failingValidator) Validate(context.Context, config.Config) error {
	return errors.New("no route")
}

type RecordingExecutor struct {
	Calls       []Call
	SideEffects int
	Err         error
	ContainerID string
}

func (r *RecordingExecutor) Run(_ context.Context, call Call) error {
	r.Calls = append(r.Calls, call)
	if r.ContainerID != "" {
		if cidfile, ok := callCIDFile(call); ok {
			if err := os.WriteFile(cidfile, []byte(r.ContainerID+"\n"), 0o600); err != nil {
				return err
			}
		}
	}
	return r.Err
}

func callCIDFile(call Call) (string, bool) {
	for i := 0; i < len(call.Args); i++ {
		arg := call.Args[i]
		if arg == "--cidfile" && i+1 < len(call.Args) {
			return call.Args[i+1], true
		}
		if value, ok := strings.CutPrefix(arg, "--cidfile="); ok {
			return value, true
		}
	}
	return "", false
}

func callHasCIDFile(call Call) bool {
	_, ok := callCIDFile(call)
	return ok
}

type recordingContainerInspector struct {
	running bool
	err     error
}

func (r recordingContainerInspector) Running(_ context.Context, _ config.Config, _ string) (bool, error) {
	return r.running, r.err
}

type recordingContainerTracker struct {
	containers []ContainerMetadata
	err        error
}

func (r recordingContainerTracker) List(_ context.Context, _ config.Config) ([]ContainerMetadata, error) {
	return r.containers, r.err
}

type remoteContainerTracker struct {
	containers map[string][]ContainerMetadata
}

func (r remoteContainerTracker) List(_ context.Context, cfg config.Config) ([]ContainerMetadata, error) {
	return r.containers[cfg.RemoteDockerHost], nil
}

type cancelingExecutor struct {
	Calls  []Call
	cancel context.CancelFunc
	err    error
}

func (r *cancelingExecutor) Run(_ context.Context, call Call) error {
	r.Calls = append(r.Calls, call)
	if r.cancel != nil {
		r.cancel()
	}
	return r.err
}

type blockingExecutor struct {
	Calls   []Call
	started chan struct{}
	release chan struct{}
	err     error
}

func (r *blockingExecutor) Run(_ context.Context, call Call) error {
	r.Calls = append(r.Calls, call)
	close(r.started)
	<-r.release
	return r.err
}

type recordingSyncDriver struct {
	status               dbsync.Status
	projections          []dbsync.Projection
	stops                int
	stopContextsCanceled int
	stopCh               chan struct{}
	stopErr              error
}

func (r *recordingSyncDriver) Start(_ context.Context, projection dbsync.Projection) (dbsync.Session, error) {
	r.projections = append(r.projections, projection)
	status := r.status
	status.ID = "sync-1"
	status.LocalPath = projection.LocalPath
	status.RemotePath = projection.RemotePath
	return &recordingSyncSession{
		status: status,
		onStop: func(ctx context.Context) {
			r.stops++
			if r.stopCh != nil {
				select {
				case r.stopCh <- struct{}{}:
				default:
				}
			}
			if ctx.Err() != nil {
				r.stopContextsCanceled++
			}
		},
		err: r.stopErr,
	}, nil
}

type recordingSyncSession struct {
	status dbsync.Status
	onStop func(context.Context)
	err    error
}

func (s *recordingSyncSession) Stop(ctx context.Context) error {
	if s.onStop != nil {
		s.onStop(ctx)
	}
	s.status.Active = false
	return s.err
}

func (s *recordingSyncSession) Status() dbsync.Status {
	return s.status
}

type failingSyncDriver struct{}

func (failingSyncDriver) Start(context.Context, dbsync.Projection) (dbsync.Session, error) {
	return nil, errors.New("sync failed")
}

type recordingRemoteCleaner struct {
	paths []string
}

func (r *recordingRemoteCleaner) Remove(_ context.Context, _ config.Config, remoteWorkspace string) error {
	r.paths = append(r.paths, remoteWorkspace)
	return nil
}

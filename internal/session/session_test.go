package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStableProjectIdentity(t *testing.T) {
	a := Identity("/Users/me/project", "ssh://dev", "web")
	b := Identity("/Users/me/project", "ssh://dev", "web")
	c := Identity("/Users/me/project", "ssh://prod", "web")

	if a != b {
		t.Fatalf("identity not stable: %s != %s", a, b)
	}
	if a == c {
		t.Fatalf("identity should include remote target")
	}
}

func TestStoreSaveLoadAndCleanup(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	s := Session{
		ID:              "abc",
		LocalRoot:       "/Users/me/project",
		RemoteTarget:    "ssh://dev",
		RemoteWorkspace: "/srv/dockbridge/abc",
		AutoRemove:      true,
		Syncs:           []SyncState{{ID: "sync-1", LocalPath: "/local", RemotePath: "/remote", Active: true, Backend: "mutagen", MutagenName: "dockbridge-abc", MutagenIdentifier: "sync_abc", LastStatus: "Watching for changes"}},
		Tunnels:         []TunnelState{{ID: "tunnel-1", LocalBind: "127.0.0.1", LocalPort: 3000, RemotePort: 49152, Active: true}},
		GeneratedFiles:  []string{"/tmp/generated.yml"},
	}

	if err := store.Save(s); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("abc")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RemoteWorkspace != s.RemoteWorkspace || !loaded.AutoRemove || len(loaded.Syncs) != 1 || len(loaded.Tunnels) != 1 {
		t.Fatalf("loaded session mismatch: %+v", loaded)
	}

	if err := store.CleanupManagedState("abc"); err != nil {
		t.Fatal(err)
	}
	cleaned, err := store.Load("abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(cleaned.Syncs) != 0 || len(cleaned.Tunnels) != 0 || len(cleaned.GeneratedFiles) != 0 {
		t.Fatalf("cleanup left managed state: %+v", cleaned)
	}
	if got := WorkspacePath("/srv/dockbridge", "abc"); got != filepath.ToSlash("/srv/dockbridge/abc") {
		t.Fatalf("WorkspacePath = %q", got)
	}

	if err := store.Delete("abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("abc"); err == nil {
		t.Fatal("expected deleted session to be missing")
	}
	if err := store.Delete("abc"); err != nil {
		t.Fatalf("deleting a missing session should be a no-op: %v", err)
	}
}

func TestStoreCleanupTerminatesMutagenSessions(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if err := store.Save(Session{
		ID: "abc",
		Syncs: []SyncState{
			{ID: "sync-1", Backend: "mutagen", MutagenName: "dockbridge-abc", Active: true},
			{ID: "sync-2", Backend: "rsync", Active: true},
		},
	}); err != nil {
		t.Fatal(err)
	}
	var terminated []string
	err := store.CleanupManagedStateWithTerminator("abc", func(sync SyncState) error {
		terminated = append(terminated, sync.MutagenName)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(terminated) != 1 || terminated[0] != "dockbridge-abc" {
		t.Fatalf("terminated = %#v", terminated)
	}
	loaded, err := store.Load("abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Syncs) != 0 {
		t.Fatalf("syncs should be cleared: %+v", loaded.Syncs)
	}
}

func TestStoreListSessionsSortedByUpdatedAt(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	older := Session{ID: "older", UpdatedAt: time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)}
	newer := Session{ID: "newer", UpdatedAt: time.Date(2026, 5, 13, 11, 0, 0, 0, time.UTC)}
	if err := store.Save(older); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(newer); err != nil {
		t.Fatal(err)
	}

	sessions, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions length = %d", len(sessions))
	}
	if sessions[0].ID != "newer" || sessions[1].ID != "older" {
		t.Fatalf("sessions not sorted newest first: %+v", sessions)
	}
}

func TestStoreListEmptyAndMalformedState(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	sessions, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("empty store returned sessions: %+v", sessions)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sessions", "bad.json"), []byte("{bad json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil {
		t.Fatal("expected malformed session error")
	}
}

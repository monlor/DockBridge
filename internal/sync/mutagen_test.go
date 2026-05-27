package sync

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestMutagenSessionNameStableAndEndpointWithPort(t *testing.T) {
	projection := Projection{
		SessionID:  "abc123",
		LocalPath:  "/Users/me/Downloads",
		RemotePath: "~/.dockbridge/workspaces/abc123/Downloads",
		RemoteHost: "ssh://monlor@nuc.monlor.cn:1023",
	}

	name := MutagenSessionName(projection)
	if name != MutagenSessionName(projection) {
		t.Fatalf("MutagenSessionName not stable")
	}
	if !strings.HasPrefix(name, "dockbridge-abc123-") {
		t.Fatalf("unexpected session name %q", name)
	}

	endpoint, err := MutagenRemoteEndpoint(projection)
	if err != nil {
		t.Fatal(err)
	}
	want := "monlor@nuc.monlor.cn:1023:~/.dockbridge/workspaces/abc123/Downloads"
	if endpoint != want {
		t.Fatalf("MutagenRemoteEndpoint = %q, want %q", endpoint, want)
	}
}

func TestMutagenRemoteEndpointUsesSCPLikeSyntaxForAbsolutePath(t *testing.T) {
	projection := Projection{
		SessionID:  "abc123",
		LocalPath:  "/Users/me/project",
		RemotePath: "/srv/dockbridge/project",
		RemoteHost: "ssh://me@example.com:2222",
	}

	endpoint, err := MutagenRemoteEndpoint(projection)
	if err != nil {
		t.Fatal(err)
	}
	want := "me@example.com:2222:/srv/dockbridge/project"
	if endpoint != want {
		t.Fatalf("MutagenRemoteEndpoint = %q, want %q", endpoint, want)
	}
	if strings.HasPrefix(endpoint, "ssh://") {
		t.Fatalf("Mutagen endpoint should use SCP-like syntax, got %q", endpoint)
	}
}

func TestMutagenDriverCreatesFlushesAndTerminatesSession(t *testing.T) {
	runner := &fakeMutagenRunner{}
	driver := MutagenDriver{
		Binary:  "mutagen",
		Mode:    "one-way-replica",
		Ignores: []string{"node_modules", ".git"},
		Runner:  runner,
	}
	projection := Projection{
		SessionID:  "abc123",
		LocalPath:  "/Users/me/project",
		RemotePath: "~/.dockbridge/workspaces/abc123/project",
		RemoteHost: "ssh://me@example.com:2222",
	}

	session, err := driver.Start(context.Background(), projection)
	if err != nil {
		t.Fatal(err)
	}
	status := session.Status()
	if !status.Active || status.Backend != "mutagen" || status.MutagenName == "" || status.RemoteEndpoint == "" {
		t.Fatalf("unexpected session status: %+v", status)
	}
	if err := session.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	calls := runner.callArgs()
	expectedPrefixes := [][]string{
		{"version"},
		{"sync", "list", status.MutagenName},
		{"sync", "create"},
		{"sync", "flush", status.MutagenName},
		{"sync", "list", status.MutagenName},
		{"sync", "terminate", status.MutagenName},
	}
	if len(calls) != len(expectedPrefixes) {
		t.Fatalf("calls = %#v", calls)
	}
	for i, prefix := range expectedPrefixes {
		if !hasPrefix(calls[i], prefix) {
			t.Fatalf("call %d = %#v, want prefix %#v", i, calls[i], prefix)
		}
	}
	create := calls[2]
	for _, want := range []string{"--name", status.MutagenName, "--mode", "one-way-replica", "--ignore-vcs", "--ignore", "node_modules", "--ignore", ".git"} {
		if !contains(create, want) {
			t.Fatalf("create args missing %q: %#v", want, create)
		}
	}
	if contains(create, "--sync-mode") {
		t.Fatalf("create args use unsupported Mutagen flag --sync-mode: %#v", create)
	}
}

func TestMutagenDriverReusesExistingHealthySession(t *testing.T) {
	runner := &fakeMutagenRunner{existing: true}
	driver := MutagenDriver{Binary: "mutagen", Runner: runner}
	projection := Projection{SessionID: "abc", LocalPath: "/local", RemotePath: "/remote", RemoteHost: "ssh://me@example.com"}

	if _, err := driver.Start(context.Background(), projection); err != nil {
		t.Fatal(err)
	}
	for _, call := range runner.callArgs() {
		if hasPrefix(call, []string{"sync", "create"}) {
			t.Fatalf("expected reuse without create, got calls %#v", runner.callArgs())
		}
	}
}

func TestMutagenDriverFailsBeforeReadyWhenCommandFails(t *testing.T) {
	runner := &fakeMutagenRunner{failOn: "flush"}
	driver := MutagenDriver{Binary: "mutagen", Runner: runner}
	_, err := driver.Start(context.Background(), Projection{SessionID: "abc", LocalPath: "/local", RemotePath: "/remote", RemoteHost: "ssh://me@example.com"})
	if err == nil || !strings.Contains(err.Error(), "Mutagen") {
		t.Fatalf("expected Mutagen readiness error, got %v", err)
	}
}

func TestMutagenDriverExplainsMacOSPrivacyDenial(t *testing.T) {
	runner := &fakeMutagenRunner{
		failOn:  "flush",
		message: "alpha scan error: unable to open synchronization root: operation not permitted",
	}
	driver := MutagenDriver{Binary: "mutagen", Runner: runner}
	_, err := driver.Start(context.Background(), Projection{
		SessionID:  "abc",
		LocalPath:  "/Users/monlor/Downloads",
		RemotePath: "/remote",
		RemoteHost: "ssh://me@example.com",
	})
	if err == nil {
		t.Fatal("expected Mutagen privacy denial error")
	}
	for _, want := range []string{"/Users/monlor/Downloads", "macOS privacy", "Full Disk Access", "mutagen daemon stop"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to contain %q, got %v", want, err)
		}
	}
}

func TestMutagenAvailabilityFailure(t *testing.T) {
	runner := &fakeMutagenRunner{failOn: "version"}
	driver := MutagenDriver{Binary: "missing-mutagen", Runner: runner}
	_, err := driver.Start(context.Background(), Projection{SessionID: "abc", LocalPath: "/local", RemotePath: "/remote", RemoteHost: "ssh://me@example.com"})
	if err == nil || !strings.Contains(err.Error(), "install or configure Mutagen") {
		t.Fatalf("expected actionable missing Mutagen error, got %v", err)
	}
}

func TestExecMutagenRunnerIncludesCommandOutputInErrors(t *testing.T) {
	runner := execMutagenRunner{}
	_, err := runner.Run(context.Background(), "sh", "-c", "printf 'unknown flag: --sync-mode' >&2; exit 1")
	if err == nil {
		t.Fatal("expected command error")
	}
	if !strings.Contains(err.Error(), "unknown flag: --sync-mode") {
		t.Fatalf("expected command output in error, got %v", err)
	}
}

type fakeMutagenRunner struct {
	calls    []mutagenCommand
	existing bool
	failOn   string
	message  string
}

func (f *fakeMutagenRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, mutagenCommand{Name: name, Args: append([]string{}, args...)})
	joined := strings.Join(args, " ")
	if f.failOn != "" && strings.Contains(joined, f.failOn) {
		if f.message != "" {
			return []byte(f.message), errors.New(f.message)
		}
		return nil, errors.New("boom")
	}
	if reflect.DeepEqual(args[:min(2, len(args))], []string{"sync", "list"}) {
		if f.existing || len(f.calls) > 3 {
			return []byte("Status: Watching for changes\nIdentifier: sync_abc\n"), nil
		}
		return nil, errors.New("not found")
	}
	return []byte("ok\n"), nil
}

func (f *fakeMutagenRunner) callArgs() [][]string {
	out := make([][]string, 0, len(f.calls))
	for _, call := range f.calls {
		out = append(out, call.Args)
	}
	return out
}

func hasPrefix(values, prefix []string) bool {
	if len(values) < len(prefix) {
		return false
	}
	for i := range prefix {
		if values[i] != prefix[i] {
			return false
		}
	}
	return true
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

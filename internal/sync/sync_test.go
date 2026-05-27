package sync

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestLocalMirrorDriverCopiesFilesAndStops(t *testing.T) {
	local := t.TempDir()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(local, "hello.txt"), []byte("world"), 0o600); err != nil {
		t.Fatal(err)
	}

	driver := LocalMirrorDriver{}
	session, err := driver.Start(context.Background(), Projection{
		SessionID:  "s1",
		LocalPath:  local,
		RemotePath: filepath.Join(remote, "project"),
	})
	if err != nil {
		t.Fatal(err)
	}

	copied, err := os.ReadFile(filepath.Join(remote, "project", "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(copied) != "world" {
		t.Fatalf("copied content = %q", copied)
	}
	if err := session.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if session.Status().Active {
		t.Fatalf("session should be inactive after stop")
	}
}

func TestLocalMirrorDriverCopiesSingleFile(t *testing.T) {
	local := filepath.Join(t.TempDir(), "config.json")
	remote := filepath.Join(t.TempDir(), "project", "config.json")
	if err := os.WriteFile(local, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	driver := LocalMirrorDriver{}
	if _, err := driver.Start(context.Background(), Projection{SessionID: "s1", LocalPath: local, RemotePath: remote}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(remote); err != nil {
		t.Fatal(err)
	}
}

func TestSSHRsyncDriverReportsCommandErrors(t *testing.T) {
	driver := SSHRsyncDriver{}
	if _, err := driver.Start(context.Background(), Projection{SessionID: "s1", LocalPath: "/missing", RemotePath: "/remote", RemoteHost: ""}); err == nil {
		t.Fatal("expected ssh/rsync setup error")
	}
}

func TestIsLocalAccessDeniedClassifiesMutagenRootAccessError(t *testing.T) {
	output := []byte("alpha scan error: unable to open synchronization root: scan failed")
	if !IsLocalAccessDeniedOutput(output) {
		t.Fatal("expected synchronization root access error to be classified as local access denied")
	}
}

func TestIsLocalAccessDeniedDoesNotClassifyStaleRemoteRoot(t *testing.T) {
	output := []byte("Transition problems:\n\t<root>: unable to open synchronization root parent directory: no such file or directory\n")
	if IsLocalAccessDeniedOutput(output) {
		t.Fatal("stale remote root should not be classified as local access denied")
	}
}

func TestSSHRsyncDriverLifecycleWithInjectedRunner(t *testing.T) {
	calls := 0
	driver := SSHRsyncDriver{
		Interval: time.Millisecond,
		Run: func(context.Context, Projection) error {
			calls++
			return nil
		},
	}
	session, err := driver.Start(context.Background(), Projection{SessionID: "s1", LocalPath: "/local", RemotePath: "/remote", RemoteHost: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Millisecond)
	if calls < 1 {
		t.Fatalf("expected at least initial sync call")
	}
	if !session.Status().Active {
		t.Fatalf("session should be active")
	}
	if err := session.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if session.Status().Active {
		t.Fatalf("session should stop")
	}
}

func TestRsyncSSHArgsParseDockerSSHURLWithPort(t *testing.T) {
	projection := Projection{
		LocalPath:  "/Users/monlor/Downloads",
		RemotePath: "~/.dockbridge/workspaces/session/Downloads",
		RemoteHost: "ssh://monlor@nuc.monlor.cn:1023",
	}
	sshArgs, err := sshMkdirArgs(projection)
	if err != nil {
		t.Fatal(err)
	}
	wantSSH := []string{"-p", "1023", "monlor@nuc.monlor.cn", "mkdir", "-p", "~/.dockbridge/workspaces/session/Downloads"}
	if !reflect.DeepEqual(sshArgs, wantSSH) {
		t.Fatalf("sshMkdirArgs = %#v, want %#v", sshArgs, wantSSH)
	}

	rsyncArgs, err := rsyncArgs(projection)
	if err != nil {
		t.Fatal(err)
	}
	wantRsync := []string{
		"-az", "--delete",
		"-e", "ssh -p 1023",
		"/Users/monlor/Downloads/",
		"monlor@nuc.monlor.cn:~/.dockbridge/workspaces/session/Downloads/",
	}
	if !reflect.DeepEqual(rsyncArgs, wantRsync) {
		t.Fatalf("rsyncArgs = %#v, want %#v", rsyncArgs, wantRsync)
	}
}

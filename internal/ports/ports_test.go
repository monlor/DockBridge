package ports

import (
	"context"
	"net"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParsePublish(t *testing.T) {
	tests := map[string]Publish{
		"3000:3000":         {HostPort: 3000, ContainerPort: 3000, Protocol: "tcp"},
		"127.0.0.1:2021:21": {HostIP: "127.0.0.1", HostPort: 2021, ContainerPort: 21, Protocol: "tcp"},
		"8080:80/udp":       {HostPort: 8080, ContainerPort: 80, Protocol: "udp"},
		"80":                {ContainerPort: 80, Protocol: "tcp"},
	}

	for input, want := range tests {
		got, err := ParsePublish(input)
		if err != nil {
			t.Fatalf("ParsePublish(%q): %v", input, err)
		}
		if got.HostIP != want.HostIP || got.HostPort != want.HostPort || got.ContainerPort != want.ContainerPort || got.Protocol != want.Protocol {
			t.Fatalf("ParsePublish(%q) = %+v, want %+v", input, got, want)
		}
	}
}

func TestPortConflictDetection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	if IsAvailable("127.0.0.1", port) {
		t.Fatalf("port %d should be unavailable", port)
	}
}

func TestFakeTunnelLifecycle(t *testing.T) {
	manager := NewMemoryTunnelManager()
	tunnel, err := manager.Start(context.Background(), TunnelSpec{
		SessionID:  "s1",
		LocalBind:  "127.0.0.1",
		LocalPort:  3000,
		RemoteHost: "127.0.0.1",
		RemotePort: 49152,
		SSHTarget:  "dev@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !tunnel.Active {
		t.Fatalf("tunnel should be active")
	}
	if err := manager.Stop(tunnel.ID); err != nil {
		t.Fatal(err)
	}
	if manager.ActiveCount() != 0 {
		t.Fatalf("tunnel should be stopped")
	}
}

func TestParsePublishErrorsAndStopEmptySSHTunnel(t *testing.T) {
	if _, err := ParsePublish("too:many:parts:here"); err == nil {
		t.Fatal("expected invalid publish error")
	}
	if _, err := ParsePublish("/Users/monlor/Downloads:/tmp"); err == nil || !strings.Contains(err.Error(), "use -v/--volume") {
		t.Fatalf("expected bind-mount hint for path-like publish, got %v", err)
	}
	if err := (SSHTunnelManager{}).StopTunnel(Tunnel{}); err != nil {
		t.Fatal(err)
	}
}

func TestSSHForwardArgsParsesDockerSSHURLWithPort(t *testing.T) {
	got, err := sshForwardArgs("ssh://monlor@nuc.monlor.cn:1023", "127.0.0.1:8080", "127.0.0.1:49152")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-N",
		"-o", "ExitOnForwardFailure=yes",
		"-L", "127.0.0.1:8080:127.0.0.1:49152",
		"-p", "1023",
		"monlor@nuc.monlor.cn",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sshForwardArgs = %#v, want %#v", got, want)
	}
}

func TestSSHForwardArgsParsesStrippedDockerSSHURLWithPort(t *testing.T) {
	got, err := sshForwardArgs("monlor@nuc.monlor.cn:1023", "127.0.0.1:8080", "127.0.0.1:49152")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-N",
		"-o", "ExitOnForwardFailure=yes",
		"-L", "127.0.0.1:8080:127.0.0.1:49152",
		"-p", "1023",
		"monlor@nuc.monlor.cn",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sshForwardArgs = %#v, want %#v", got, want)
	}
}

func TestParseSSHTargetVariants(t *testing.T) {
	tests := []struct {
		input    string
		wantHost string
		wantPort string
		wantErr  bool
	}{
		{"ssh://me@example.com:2200", "me@example.com", "2200", false},
		{"me@example.com:2200", "me@example.com", "2200", false},
		{"example.com:2200", "example.com", "2200", false},
		{"ssh://", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			host, port, err := parseSSHTarget(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if host != tt.wantHost || port != tt.wantPort {
				t.Fatalf("parseSSHTarget = %q %q, want %q %q", host, port, tt.wantHost, tt.wantPort)
			}
		})
	}
}

func TestWaitForForwardReportsEarlyExit(t *testing.T) {
	cmd := exec.Command("false")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	err := waitForForward(context.Background(), cmd, "127.0.0.1", 1)
	if err == nil || !strings.Contains(err.Error(), "ssh tunnel exited") {
		t.Fatalf("expected early exit error, got %v", err)
	}
}

func TestWaitForForwardReportsTimeout(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sleep", "1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	err = waitForForward(ctx, cmd, "127.0.0.1", port)
	if err == nil {
		t.Fatal("expected timeout/context error")
	}
	_ = cmd.Process.Kill()
}

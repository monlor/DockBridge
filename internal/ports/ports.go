package ports

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	stdsync "sync"
	"syscall"
	"time"
)

type Publish struct {
	HostIP        string
	HostPort      int
	ContainerPort int
	Protocol      string
}

func ParsePublish(input string) (Publish, error) {
	if looksLikeBindMountPublish(input) {
		return Publish{}, fmt.Errorf("invalid publish spec %q: value looks like a bind mount; use -v/--volume instead of -p/--publish", input)
	}
	value, protocol, _ := strings.Cut(input, "/")
	if protocol == "" {
		protocol = "tcp"
	}
	if !validPublishProtocol(protocol) {
		return Publish{}, fmt.Errorf("invalid publish protocol %q in %q; expected tcp, udp, or sctp", protocol, input)
	}
	parts := strings.Split(value, ":")
	parse := func(s string) (int, error) {
		if s == "" {
			return 0, nil
		}
		return strconv.Atoi(s)
	}

	switch len(parts) {
	case 1:
		container, err := parse(parts[0])
		return Publish{ContainerPort: container, Protocol: protocol}, err
	case 2:
		host, err := parse(parts[0])
		if err != nil {
			return Publish{}, err
		}
		container, err := parse(parts[1])
		return Publish{HostPort: host, ContainerPort: container, Protocol: protocol}, err
	case 3:
		host, err := parse(parts[1])
		if err != nil {
			return Publish{}, err
		}
		container, err := parse(parts[2])
		return Publish{HostIP: parts[0], HostPort: host, ContainerPort: container, Protocol: protocol}, err
	default:
		return Publish{}, fmt.Errorf("invalid publish spec %q", input)
	}
}

func validPublishProtocol(protocol string) bool {
	switch protocol {
	case "tcp", "udp", "sctp":
		return true
	default:
		return false
	}
}

func looksLikeBindMountPublish(input string) bool {
	parts := strings.Split(input, ":")
	if len(parts) < 2 {
		return false
	}
	source := parts[0]
	target := parts[1]
	if target == "" || !strings.HasPrefix(target, "/") {
		return false
	}
	return strings.HasPrefix(source, "/") || strings.HasPrefix(source, "~") || strings.HasPrefix(source, ".")
}

func IsAvailable(bind string, port int) bool {
	listener, err := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

type TunnelSpec struct {
	SessionID  string
	LocalBind  string
	LocalPort  int
	RemoteHost string
	RemotePort int
	SSHTarget  string
}

type Tunnel struct {
	ID     string
	Spec   TunnelSpec
	Active bool
	PID    int
	cmd    *exec.Cmd
}

type MemoryTunnelManager struct {
	mu      stdsync.Mutex
	tunnels map[string]Tunnel
}

func NewMemoryTunnelManager() *MemoryTunnelManager {
	return &MemoryTunnelManager{tunnels: map[string]Tunnel{}}
}

func (m *MemoryTunnelManager) Start(_ context.Context, spec TunnelSpec) (Tunnel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := fmt.Sprintf("%s:%s:%d", spec.SessionID, spec.LocalBind, spec.LocalPort)
	tunnel := Tunnel{ID: id, Spec: spec, Active: true}
	m.tunnels[id] = tunnel
	return tunnel, nil
}

func (m *MemoryTunnelManager) Stop(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tunnels, id)
	return nil
}

func (m *MemoryTunnelManager) StopTunnel(tunnel Tunnel) error {
	return m.Stop(tunnel.ID)
}

func (m *MemoryTunnelManager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.tunnels)
}

type SSHTunnelManager struct{}

func (SSHTunnelManager) Start(ctx context.Context, spec TunnelSpec) (Tunnel, error) {
	id := fmt.Sprintf("%s:%s:%d", spec.SessionID, spec.LocalBind, spec.LocalPort)
	local := net.JoinHostPort(spec.LocalBind, strconv.Itoa(spec.LocalPort))
	remote := net.JoinHostPort(spec.RemoteHost, strconv.Itoa(spec.RemotePort))
	args, err := sshForwardArgs(spec.SSHTarget, local, remote)
	if err != nil {
		return Tunnel{}, err
	}
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return Tunnel{}, err
	}
	if err := waitForForward(ctx, cmd, spec.LocalBind, spec.LocalPort); err != nil {
		_ = cmd.Process.Kill()
		return Tunnel{}, err
	}
	return Tunnel{ID: id, Spec: spec, Active: true, PID: cmd.Process.Pid, cmd: cmd}, nil
}

func (m SSHTunnelManager) StopTunnel(tunnel Tunnel) error {
	if tunnel.cmd != nil && tunnel.cmd.Process != nil {
		return killTunnelProcess(tunnel.cmd.Process.Pid)
	}
	if tunnel.PID != 0 {
		return killTunnelProcess(tunnel.PID)
	}
	return nil
}

func killTunnelProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func sshForwardArgs(target, local, remote string) ([]string, error) {
	sshTarget, port, err := parseSSHTarget(target)
	if err != nil {
		return nil, err
	}
	args := []string{"-N", "-o", "ExitOnForwardFailure=yes", "-L", local + ":" + remote}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, sshTarget)
	return args, nil
}

func parseSSHTarget(target string) (string, string, error) {
	if strings.HasPrefix(target, "ssh://") {
		parsed, err := url.Parse(target)
		if err != nil {
			return "", "", err
		}
		host := parsed.Hostname()
		if host == "" {
			return "", "", fmt.Errorf("invalid ssh target %q", target)
		}
		if parsed.User != nil {
			host = parsed.User.String() + "@" + host
		}
		return host, parsed.Port(), nil
	}
	userHost := target
	port := ""
	if at := strings.LastIndex(target, "@"); at >= 0 {
		hostPart := target[at+1:]
		if host, parsedPort, ok := strings.Cut(hostPart, ":"); ok && parsedPort != "" {
			userHost = target[:at+1] + host
			port = parsedPort
		}
	} else if host, parsedPort, ok := strings.Cut(target, ":"); ok && parsedPort != "" {
		userHost = host
		port = parsedPort
	}
	if userHost == "" {
		return "", "", fmt.Errorf("invalid ssh target %q", target)
	}
	return userHost, port, nil
}

func waitForForward(ctx context.Context, cmd *exec.Cmd, bind string, port int) error {
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	exited := make(chan error, 1)
	go func() {
		exited <- cmd.Wait()
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-exited:
			if err != nil {
				return fmt.Errorf("ssh tunnel exited before forwarding %s:%d: %w", bind, port, err)
			}
			return fmt.Errorf("ssh tunnel exited before forwarding %s:%d", bind, port)
		case <-ticker.C:
			if !IsAvailable(bind, port) {
				return nil
			}
		case <-deadline.C:
			return fmt.Errorf("ssh tunnel did not start listening on %s:%d", bind, port)
		}
	}
}

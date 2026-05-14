package sync

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	stdsync "sync"
	"time"
)

type Projection struct {
	SessionID  string
	LocalPath  string
	RemotePath string
	RemoteHost string
}

type Status struct {
	ID                string
	LocalPath         string
	RemotePath        string
	Active            bool
	Backend           string
	MutagenName       string
	MutagenIdentifier string
	RemoteEndpoint    string
	LastStatus        string
}

type Session interface {
	Stop(context.Context) error
	Status() Status
}

type Driver interface {
	Start(context.Context, Projection) (Session, error)
}

type localSession struct {
	mu     stdsync.Mutex
	status Status
}

func (s *localSession) Stop(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Active = false
	return nil
}

func (s *localSession) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

type LocalMirrorDriver struct{}

func (LocalMirrorDriver) Start(_ context.Context, projection Projection) (Session, error) {
	if err := copyTree(projection.LocalPath, projection.RemotePath); err != nil {
		return nil, err
	}
	return &localSession{status: newStatus(projection)}, nil
}

type SSHRsyncDriver struct {
	Run      func(context.Context, Projection) error
	Interval time.Duration
}

func (d SSHRsyncDriver) Start(ctx context.Context, projection Projection) (Session, error) {
	run := d.Run
	if run == nil {
		run = runRsyncOnce
	}
	interval := d.Interval
	if interval == 0 {
		interval = 2 * time.Second
	}
	if err := run(ctx, projection); err != nil {
		return nil, err
	}
	session := &pollingSession{
		status: Status{
			ID:         projection.SessionID + ":" + projection.LocalPath,
			LocalPath:  projection.LocalPath,
			RemotePath: projection.RemotePath,
			Active:     true,
		},
		cancel: func() {},
	}
	pollCtx, cancel := context.WithCancel(context.Background())
	session.cancel = cancel
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
				_ = run(ctx, projection)
			}
		}
	}()
	return session, nil
}

type pollingSession struct {
	mu     stdsync.Mutex
	status Status
	cancel func()
}

func (s *pollingSession) Stop(context.Context) error {
	s.cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Active = false
	return nil
}

func (s *pollingSession) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func runRsyncOnce(ctx context.Context, projection Projection) error {
	sshArgs, err := sshMkdirArgs(projection)
	if err != nil {
		return err
	}
	if err := exec.CommandContext(ctx, "ssh", sshArgs...).Run(); err != nil {
		return err
	}
	rsyncArgs, err := rsyncArgs(projection)
	if err != nil {
		return err
	}
	return exec.CommandContext(ctx, "rsync", rsyncArgs...).Run()
}

func newStatus(projection Projection) Status {
	return Status{
		ID:         projection.SessionID + ":" + projection.LocalPath,
		LocalPath:  projection.LocalPath,
		RemotePath: projection.RemotePath,
		Active:     true,
	}
}

type remoteEndpoint struct {
	Target string
	Port   string
}

func sshMkdirArgs(projection Projection) ([]string, error) {
	endpoint, err := parseRemoteEndpoint(projection.RemoteHost)
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, 6)
	if endpoint.Port != "" {
		args = append(args, "-p", endpoint.Port)
	}
	args = append(args, endpoint.Target, "mkdir", "-p", projection.RemotePath)
	return args, nil
}

func rsyncArgs(projection Projection) ([]string, error) {
	endpoint, err := parseRemoteEndpoint(projection.RemoteHost)
	if err != nil {
		return nil, err
	}
	source := filepath.Clean(projection.LocalPath) + string(os.PathSeparator)
	destination := endpoint.Target + ":" + strings.TrimRight(projection.RemotePath, "/") + "/"
	args := []string{"-az", "--delete"}
	if endpoint.Port != "" {
		args = append(args, "-e", "ssh -p "+endpoint.Port)
	}
	args = append(args, source, destination)
	return args, nil
}

func parseRemoteEndpoint(remoteHost string) (remoteEndpoint, error) {
	if strings.HasPrefix(remoteHost, "ssh://") {
		parsed, err := url.Parse(remoteHost)
		if err != nil {
			return remoteEndpoint{}, err
		}
		host := parsed.Hostname()
		if host == "" {
			return remoteEndpoint{}, fmt.Errorf("invalid remote host %q", remoteHost)
		}
		if parsed.User != nil {
			host = parsed.User.String() + "@" + host
		}
		return remoteEndpoint{Target: host, Port: parsed.Port()}, nil
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
		return remoteEndpoint{}, fmt.Errorf("invalid remote host %q", remoteHost)
	}
	return remoteEndpoint{Target: target, Port: port}, nil
}

func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		return copyFile(src, dst, info.Mode())
	}
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

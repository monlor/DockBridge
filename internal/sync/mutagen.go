package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	stdsync "sync"
)

type MutagenRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type mutagenCommand struct {
	Name string
	Args []string
}

type execMutagenRunner struct{}

func (execMutagenRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return output, err
		}
		return output, fmt.Errorf("%w: %s", err, message)
	}
	return output, nil
}

type MutagenDriver struct {
	Binary  string
	Mode    string
	Ignores []string
	Runner  MutagenRunner
}

func (d MutagenDriver) Start(ctx context.Context, projection Projection) (Session, error) {
	binary := d.Binary
	if binary == "" {
		binary = "mutagen"
	}
	mode := d.Mode
	if mode == "" {
		mode = "one-way-replica"
	}
	runner := d.Runner
	if runner == nil {
		runner = execMutagenRunner{}
	}
	if _, err := runner.Run(ctx, binary, "version"); err != nil {
		return nil, fmt.Errorf("Mutagen is required for file projection; install or configure Mutagen with DOCKBRIDGE_MUTAGEN_BIN: %w", err)
	}

	name := MutagenSessionName(projection)
	endpoint, err := MutagenRemoteEndpoint(projection)
	if err != nil {
		return nil, err
	}
	statusOutput, err := runner.Run(ctx, binary, "sync", "list", name)
	if err != nil {
		createArgs := mutagenCreateArgs(name, mode, d.Ignores, projection.LocalPath, endpoint)
		if _, err := runner.Run(ctx, binary, createArgs...); err != nil {
			return nil, fmt.Errorf("Mutagen session creation failed for %s: %w", projection.LocalPath, err)
		}
	}
	if _, err := runner.Run(ctx, binary, "sync", "flush", name); err != nil {
		return nil, fmt.Errorf("Mutagen readiness failed for %s during flush: %w", projection.LocalPath, err)
	}
	statusOutput, err = runner.Run(ctx, binary, "sync", "list", name)
	if err != nil {
		return nil, fmt.Errorf("Mutagen readiness failed for %s during status: %w", projection.LocalPath, err)
	}

	status := newStatus(projection)
	status.ID = name
	status.Backend = "mutagen"
	status.MutagenName = name
	status.MutagenIdentifier = parseMutagenIdentifier(string(statusOutput))
	status.RemoteEndpoint = endpoint
	status.LastStatus = parseMutagenStatus(string(statusOutput))
	return &mutagenSession{binary: binary, runner: runner, status: status}, nil
}

type mutagenSession struct {
	mu     stdsync.Mutex
	binary string
	runner MutagenRunner
	status Status
}

func (s *mutagenSession) Stop(ctx context.Context) error {
	s.mu.Lock()
	name := s.status.MutagenName
	s.mu.Unlock()
	if name == "" {
		return nil
	}
	if _, err := s.runner.Run(ctx, s.binary, "sync", "terminate", name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Active = false
	return nil
}

func (s *mutagenSession) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func MutagenSessionName(projection Projection) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		projection.SessionID,
		projection.LocalPath,
		projection.RemotePath,
		projection.RemoteHost,
	}, "\x00")))
	return "dockbridge-" + projection.SessionID + "-" + hex.EncodeToString(sum[:])[:12]
}

func MutagenRemoteEndpoint(projection Projection) (string, error) {
	endpoint, err := parseRemoteEndpoint(projection.RemoteHost)
	if err != nil {
		return "", err
	}
	if projection.RemotePath == "" {
		return "", errors.New("remote path is required")
	}
	if endpoint.Port != "" {
		return endpoint.Target + ":" + endpoint.Port + ":" + projection.RemotePath, nil
	}
	return endpoint.Target + ":" + projection.RemotePath, nil
}

func mutagenCreateArgs(name, mode string, ignores []string, localPath, remoteEndpoint string) []string {
	args := []string{"sync", "create", "--name", name, "--mode", mode, "--ignore-vcs"}
	for _, ignore := range ignores {
		args = append(args, "--ignore", ignore)
	}
	return append(args, localPath, remoteEndpoint)
}

func parseMutagenIdentifier(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if value, ok := strings.CutPrefix(line, "Identifier:"); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func parseMutagenStatus(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if value, ok := strings.CutPrefix(line, "Status:"); ok {
			return strings.TrimSpace(value)
		}
	}
	return strings.TrimSpace(output)
}

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"dockbridge/internal/cli"
	"dockbridge/internal/compose"
	"dockbridge/internal/config"
	"dockbridge/internal/ports"
	"dockbridge/internal/session"
	dbsync "dockbridge/internal/sync"
)

type Invocation struct {
	Entrypoint string
	Args       []string
	Env        map[string]string
	Config     config.Config
	Cwd        string
}

type Call struct {
	Name                    string
	Args                    []string
	Env                     []string
	SharedTerminalProcessPG bool
}

type Executor interface {
	Run(context.Context, Call) error
}

type OSExecutor struct{}

func (OSExecutor) Run(ctx context.Context, call Call) error {
	cmd := exec.Command(call.Name, call.Args...)
	cmd.Env = append(os.Environ(), call.Env...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if !call.SharedTerminalProcessPG {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	select {
	case err := <-done:
		if err == nil {
			return waitForLateCancellation(ctx)
		}
		return err
	case <-ctx.Done():
		signalChild(cmd.Process.Pid, syscall.SIGINT, call.SharedTerminalProcessPG)
		grace := time.NewTimer(10 * time.Second)
		defer grace.Stop()
		select {
		case <-done:
			return ctx.Err()
		case <-grace.C:
			signalChild(cmd.Process.Pid, syscall.SIGKILL, call.SharedTerminalProcessPG)
			<-done
			return ctx.Err()
		}
	}
}

func signalChild(pid int, sig syscall.Signal, sharedProcessGroup bool) {
	if sharedProcessGroup {
		process, err := os.FindProcess(pid)
		if err == nil {
			_ = process.Signal(sig)
		}
		return
	}
	signalProcessGroup(pid, sig)
}

func signalProcessGroup(pid int, sig syscall.Signal) {
	if pid <= 0 {
		return
	}
	if err := syscall.Kill(-pid, sig); err == nil {
		return
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = process.Signal(sig)
}

func waitForLateCancellation(ctx context.Context) error {
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type App struct {
	Executor           Executor
	SyncDriver         dbsync.Driver
	TunnelStarter      TunnelStarter
	SessionStore       SessionStore
	RemoteCleaner      RemoteWorkspaceCleaner
	RemoteValidator    RemoteValidator
	ContainerInspector ContainerInspector
	Output             io.Writer
	MutagenTerminator  func(context.Context, string, string) error
	PortAvailable      func(string, int) bool
	RemotePortStart    int
}

type startedSync struct {
	session dbsync.Session
	state   session.SyncState
}

type TunnelStarter interface {
	Start(context.Context, ports.TunnelSpec) (ports.Tunnel, error)
	StopTunnel(ports.Tunnel) error
}

type SessionStore interface {
	Save(session.Session) error
	Load(string) (session.Session, error)
	List() ([]session.Session, error)
	Delete(string) error
}

type RemoteValidator interface {
	Validate(context.Context, config.Config) error
}

type RemoteWorkspaceCleaner interface {
	Remove(context.Context, config.Config, string) error
}

type ContainerInspector interface {
	Running(context.Context, config.Config, string) (bool, error)
}

func (a App) Run(ctx context.Context, inv Invocation) error {
	executor := a.Executor
	if executor == nil {
		executor = OSExecutor{}
	}
	if isNativeEntrypoint(inv.Entrypoint) && len(inv.Args) > 0 && inv.Args[0] == "sessions" {
		return a.runNativeSessions(ctx, inv)
	}
	if inv.Config.RealDockerPath == "" {
		return errors.New("real docker path is required")
	}
	if inv.Env["DOCKBRIDGE_BYPASS"] == "1" {
		return executor.Run(ctx, Call{Name: inv.Config.RealDockerPath, Args: inv.Args})
	}

	classification := cli.Classify(inv.Entrypoint, inv.Args)
	if classification.Kind == cli.KindUnsupported {
		return errors.New(classification.UnsupportedReason)
	}
	if err := validateLocalCommand(classification, inv.Args); err != nil {
		return err
	}
	if inv.Config.RemoteDockerHost == "" {
		return errors.New("remote Docker host is required; set DOCKBRIDGE_REMOTE or DOCKER_HOST")
	}
	if a.RemoteValidator != nil {
		if err := a.RemoteValidator.Validate(ctx, inv.Config); err != nil {
			return fmt.Errorf("remote Docker connectivity failed: %w", err)
		}
	}

	switch classification.Kind {
	case cli.KindPassthrough:
		if len(inv.Args) > 0 && inv.Args[0] == "context" {
			return executor.Run(ctx, Call{Name: inv.Config.RealDockerPath, Args: inv.Args})
		}
		restored := false
		if shouldRestoreBeforeDockerCommand(inv.Args) {
			if err := a.restoreManagedSession(ctx, inv); err != nil {
				return err
			}
			restored = true
		}
		err := executor.Run(ctx, Call{Name: inv.Config.RealDockerPath, Args: append([]string{"-H", inv.Config.RemoteDockerHost}, inv.Args...)})
		if err != nil {
			if restored {
				if cleanupErr := a.suspendManagedSession(ctx, inv); cleanupErr != nil {
					return fmt.Errorf("%w; restored session cleanup failed: %v", err, cleanupErr)
				}
			}
			return err
		}
		switch dockerLifecycleAction(inv.Args) {
		case lifecycleSuspend:
			return a.suspendManagedSession(ctx, inv)
		case lifecyclePurge:
			return a.purgeManagedSession(ctx, inv)
		}
		return nil
	case cli.KindCompose:
		return a.runCompose(ctx, executor, inv)
	case cli.KindDockerRun:
		return a.runDockerRun(ctx, executor, inv)
	case cli.KindDockerBuild:
		return a.runDockerBuild(ctx, executor, inv)
	default:
		return fmt.Errorf("unsupported classification %s", classification.Kind)
	}
}

func validateLocalCommand(classification cli.Classification, args []string) error {
	if classification.Kind != cli.KindDockerRun {
		return nil
	}
	publishSpecs, err := cli.ParseRunPublishes(args)
	if err != nil {
		return err
	}
	for _, spec := range publishSpecs {
		if _, err := ports.ParsePublish(spec); err != nil {
			return err
		}
	}
	return nil
}

func (a App) runDockerBuild(ctx context.Context, executor Executor, inv Invocation) error {
	cwd := workingDir(inv.Cwd)
	sess := a.ensureSession(inv, cwd)
	contextPath := cli.BuildContext(inv.Args, cwd)
	remoteContext := filepath.ToSlash(filepath.Join(sess.RemoteWorkspace, "build-context"))
	pathMappings := map[string]string{contextPath: remoteContext}
	syncs, err := a.startSyncs(ctx, sess.ID, inv.Config, pathMappings)
	if err != nil {
		return err
	}
	rewritten, err := cli.RewriteBuildArgs(inv.Args, cwd, pathMappings)
	if err != nil {
		_ = a.stopStartedSyncs(ctx, syncs)
		return err
	}
	sess.Syncs = append(sess.Syncs, syncStates(syncs)...)
	a.saveSession(sess)
	if err := executor.Run(ctx, Call{Name: inv.Config.RealDockerPath, Args: append([]string{"-H", inv.Config.RemoteDockerHost}, rewritten...)}); err != nil {
		if cleanupErr := a.rollbackStartedResources(ctx, syncs, nil); cleanupErr != nil {
			return fmt.Errorf("%w; startup cleanup failed: %v", err, cleanupErr)
		}
		deactivateSessionState(&sess)
		a.saveSession(sess)
		return err
	}
	return nil
}

func (a App) runCompose(ctx context.Context, executor Executor, inv Invocation) error {
	cwd := workingDir(inv.Cwd)
	composeArgs := inv.Args
	if len(inv.Args) > 0 && inv.Args[0] == "compose" {
		composeArgs = inv.Args[1:]
	}
	if action := composeLifecycleAction(composeArgs); action != lifecycleNone {
		return a.runComposeLifecycle(ctx, executor, inv, composeArgs, action)
	}
	files, projectDir, err := compose.DiscoverFiles(cwd, composeArgs)
	if err == nil && shouldTranslateCompose(composeArgs) {
		sess := a.ensureSession(inv, projectDir)
		generated := filepath.Join(inv.Config.StateDir, "generated", sess.ID, "compose.yml")
		translated, err := compose.Translate(compose.TranslateRequest{
			Files:           files,
			ProjectDir:      projectDir,
			RemoteRoot:      sess.RemoteWorkspace,
			RemotePortStart: a.remotePortStart(),
			OutputPath:      generated,
		})
		if err != nil {
			return err
		}
		syncs, err := a.startSyncs(ctx, sess.ID, inv.Config, translated.FileMappings)
		if err != nil {
			return err
		}
		tunnels, err := a.startTunnels(ctx, sess.ID, inv.Config, translated.PortMappings)
		if err != nil {
			_ = a.stopStartedSyncs(ctx, syncs)
			return err
		}
		sess.GeneratedFiles = append(sess.GeneratedFiles, generated)
		sess.Syncs = append(sess.Syncs, syncStates(syncs)...)
		sess.Tunnels = append(sess.Tunnels, tunnelStates(tunnels)...)
		a.saveSession(sess)
		cleanup := a.startedResourceCleanup(&sess, syncs, tunnels)
		stopCleanupWatcher := noop
		if shouldCleanupComposeAfterExit(composeArgs) {
			stopCleanupWatcher = cleanupOnContextDone(ctx, cleanup)
			defer stopCleanupWatcher()
		}
		args := append([]string{"-H", inv.Config.RemoteDockerHost, "compose", "-f", generated}, stripFileFlags(composeArgs)...)
		if err := executor.Run(ctx, Call{Name: inv.Config.RealDockerPath, Args: args}); err != nil {
			if cleanupErr := cleanup(); cleanupErr != nil {
				return fmt.Errorf("%w; startup cleanup failed: %v", err, cleanupErr)
			}
			return err
		}
		if shouldCleanupComposeAfterExit(composeArgs) {
			return cleanup()
		}
		return nil
	}
	return executor.Run(ctx, Call{
		Name: inv.Config.RealDockerPath,
		Args: append([]string{"-H", inv.Config.RemoteDockerHost}, inv.Args...),
	})
}

func (a App) runDockerRun(ctx context.Context, executor Executor, inv Invocation) error {
	if len(inv.Args) == 0 || inv.Args[0] != "run" {
		return executor.Run(ctx, Call{Name: inv.Config.RealDockerPath, Args: append([]string{"-H", inv.Config.RemoteDockerHost}, inv.Args...)})
	}
	cwd := workingDir(inv.Cwd)
	sess := a.ensureSession(inv, cwd)
	mounts, err := cli.ParseRunMounts(inv.Args, cwd)
	if err != nil {
		return err
	}
	pathMappings := map[string]string{}
	for _, mount := range mounts {
		if mount.IsNamedVolume || mount.Source == "" {
			continue
		}
		remote := filepath.ToSlash(filepath.Join(sess.RemoteWorkspace, filepath.Base(mount.Source)))
		pathMappings[mount.Source] = remote
	}
	syncs, err := a.startSyncs(ctx, sess.ID, inv.Config, pathMappings)
	if err != nil {
		return err
	}

	publishSpecs, err := cli.ParseRunPublishes(inv.Args)
	if err != nil {
		_ = a.stopStartedSyncs(ctx, syncs)
		return err
	}
	portMappings := map[int]int{}
	nextRemote := a.remotePortStart()
	for _, spec := range publishSpecs {
		publish, err := ports.ParsePublish(spec)
		if err != nil {
			return err
		}
		if publish.HostPort == 0 {
			continue
		}
		if !a.isPortAvailable(inv.Config.LocalBindAddress, publish.HostPort) {
			_ = a.stopStartedSyncs(ctx, syncs)
			return fmt.Errorf("local port conflict on %s:%d", inv.Config.LocalBindAddress, publish.HostPort)
		}
		portMappings[publish.HostPort] = nextRemote
		nextRemote++
	}
	tunnels, err := a.startTunnels(ctx, sess.ID, inv.Config, portMappings)
	if err != nil {
		_ = a.stopStartedSyncs(ctx, syncs)
		return err
	}
	rewritten, err := cli.RewriteRunArgs(inv.Args, cwd, pathMappings, portMappings)
	if err != nil {
		_ = a.rollbackStartedResources(ctx, syncs, tunnels)
		return err
	}
	interactiveLifecycle, err := a.prepareInteractiveRunLifecycle(inv.Config, sess.ID, inv.Args, &rewritten, len(syncs) > 0 || len(tunnels) > 0)
	if err != nil {
		_ = a.rollbackStartedResources(ctx, syncs, tunnels)
		return err
	}
	defer interactiveLifecycle.cleanup()
	sess.Syncs = append(sess.Syncs, syncStates(syncs)...)
	sess.Tunnels = append(sess.Tunnels, tunnelStates(tunnels)...)
	a.saveSession(sess)
	cleanup := a.startedResourceCleanup(&sess, syncs, tunnels)
	stopCleanupWatcher := noop
	if shouldWatchDockerRunCleanup(inv.Args) {
		stopCleanupWatcher = cleanupOnContextDone(ctx, cleanup)
		defer stopCleanupWatcher()
	}
	if err := executor.Run(ctx, Call{Name: inv.Config.RealDockerPath, Args: append([]string{"-H", inv.Config.RemoteDockerHost}, rewritten...), SharedTerminalProcessPG: isInteractiveDockerRun(inv.Args)}); err != nil {
		if cleanupErr := cleanup(); cleanupErr != nil {
			return fmt.Errorf("%w; startup cleanup failed: %v", err, cleanupErr)
		}
		return err
	}
	shouldCleanup, err := a.shouldCleanupSuccessfulDockerRun(inv.Config, inv.Args, ctx.Err() != nil, interactiveLifecycle)
	if err != nil {
		if cleanupErr := cleanup(); cleanupErr != nil {
			return fmt.Errorf("%w; cleanup failed: %v", err, cleanupErr)
		}
		return err
	}
	if shouldCleanup {
		return cleanup()
	}
	return nil
}

func (a App) ensureSession(inv Invocation, localRoot string) session.Session {
	id := session.Identity(localRoot, inv.Config.RemoteDockerHost, filepath.Base(localRoot))
	return session.Session{
		ID:              id,
		LocalRoot:       localRoot,
		RemoteTarget:    inv.Config.RemoteDockerHost,
		RemoteWorkspace: session.WorkspacePath(inv.Config.RemoteWorkspaceRoot, id),
	}
}

func (a App) startSyncs(ctx context.Context, sessionID string, cfg config.Config, mappings map[string]string) ([]startedSync, error) {
	if a.SyncDriver == nil {
		return nil, nil
	}
	started := make([]startedSync, 0, len(mappings))
	for local, remote := range mappings {
		syncSession, err := a.SyncDriver.Start(ctx, dbsync.Projection{SessionID: sessionID, LocalPath: local, RemotePath: remote, RemoteHost: cfg.RemoteDockerHost})
		if err != nil {
			_ = a.stopStartedSyncs(ctx, started)
			return nil, err
		}
		status := syncSession.Status()
		started = append(started, startedSync{
			session: syncSession,
			state: session.SyncState{
				ID:                status.ID,
				LocalPath:         status.LocalPath,
				RemotePath:        status.RemotePath,
				Active:            status.Active,
				Backend:           status.Backend,
				MutagenName:       status.MutagenName,
				MutagenIdentifier: status.MutagenIdentifier,
				RemoteEndpoint:    status.RemoteEndpoint,
				LastStatus:        status.LastStatus,
			},
		})
	}
	return started, nil
}

func (a App) startTunnels(ctx context.Context, sessionID string, cfg config.Config, mappings map[int]int) ([]ports.Tunnel, error) {
	if a.TunnelStarter == nil {
		return nil, nil
	}
	tunnels := make([]ports.Tunnel, 0, len(mappings))
	for local, remote := range mappings {
		if !a.isPortAvailable(cfg.LocalBindAddress, local) {
			return nil, fmt.Errorf("local port conflict on %s:%d", cfg.LocalBindAddress, local)
		}
		tunnel, err := a.TunnelStarter.Start(ctx, ports.TunnelSpec{
			SessionID:  sessionID,
			LocalBind:  cfg.LocalBindAddress,
			LocalPort:  local,
			RemoteHost: "127.0.0.1",
			RemotePort: remote,
			SSHTarget:  cfg.RemoteDockerHost,
		})
		if err != nil {
			a.stopTunnels(tunnels)
			return nil, err
		}
		tunnels = append(tunnels, tunnel)
	}
	return tunnels, nil
}

func (a App) stopTunnels(tunnels []ports.Tunnel) {
	if a.TunnelStarter == nil {
		return
	}
	for i := len(tunnels) - 1; i >= 0; i-- {
		_ = a.TunnelStarter.StopTunnel(tunnels[i])
	}
}

func (a App) stopStartedSyncs(ctx context.Context, syncs []startedSync) error {
	var errs []error
	for i := len(syncs) - 1; i >= 0; i-- {
		if syncs[i].session == nil {
			continue
		}
		if err := syncs[i].session.Stop(ctx); err != nil {
			if isMissingMutagenSyncError(err) {
				continue
			}
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (a App) rollbackStartedResources(ctx context.Context, syncs []startedSync, tunnels []ports.Tunnel) error {
	a.stopTunnels(tunnels)
	return a.stopStartedSyncs(ctx, syncs)
}

func (a App) startedResourceCleanup(sess *session.Session, syncs []startedSync, tunnels []ports.Tunnel) func() error {
	var once sync.Once
	var err error
	return func() error {
		once.Do(func() {
			err = a.suspendStartedResources(sess, syncs, tunnels)
		})
		return err
	}
}

func cleanupOnContextDone(ctx context.Context, cleanup func() error) func() {
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			_ = cleanup()
		case <-done:
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

func noop() {}

type interactiveRunLifecycle struct {
	enabled bool
	cidfile string
	owned   bool
}

func (l interactiveRunLifecycle) cleanup() {
	if l.owned && l.cidfile != "" {
		_ = os.Remove(l.cidfile)
	}
}

func (a App) prepareInteractiveRunLifecycle(cfg config.Config, sessionID string, originalArgs []string, rewritten *[]string, hasManagedResources bool) (interactiveRunLifecycle, error) {
	if !hasManagedResources || !isInteractiveDockerRun(originalArgs) || isDetachedDockerRun(originalArgs) {
		return interactiveRunLifecycle{}, nil
	}
	cidfile, ok, err := cli.RunCIDFile(*rewritten)
	if err != nil {
		return interactiveRunLifecycle{}, err
	}
	if ok {
		return interactiveRunLifecycle{enabled: true, cidfile: cidfile}, nil
	}
	cidfile, err = internalCIDFilePath(cfg.StateDir, sessionID)
	if err != nil {
		return interactiveRunLifecycle{}, err
	}
	*rewritten = cli.InjectRunCIDFile(*rewritten, cidfile)
	return interactiveRunLifecycle{enabled: true, cidfile: cidfile, owned: true}, nil
}

func internalCIDFilePath(stateDir, sessionID string) (string, error) {
	if stateDir == "" {
		stateDir = filepath.Join(os.TempDir(), "dockbridge")
	}
	dir := filepath.Join(stateDir, "cidfiles", sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("run-%d.cid", time.Now().UnixNano())), nil
}

func (a App) shouldCleanupSuccessfulDockerRun(cfg config.Config, args []string, interrupted bool, lifecycle interactiveRunLifecycle) (bool, error) {
	if shouldCleanupDockerRunAfterExit(args, interrupted) {
		return true, nil
	}
	if !lifecycle.enabled {
		return false, nil
	}
	containerID, err := readContainerID(lifecycle.cidfile)
	if err != nil || containerID == "" {
		return true, nil
	}
	inspector := a.ContainerInspector
	if inspector == nil {
		inspector = DockerContainerInspector{}
	}
	inspectCtx, cancel := cleanupContext()
	defer cancel()
	running, err := inspector.Running(inspectCtx, cfg, containerID)
	if err != nil {
		return true, nil
	}
	return !running, nil
}

func readContainerID(cidfile string) (string, error) {
	if cidfile == "" {
		return "", nil
	}
	data, err := os.ReadFile(cidfile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

type DockerContainerInspector struct{}

func (DockerContainerInspector) Running(ctx context.Context, cfg config.Config, containerID string) (bool, error) {
	output, err := exec.CommandContext(ctx, cfg.RealDockerPath, "-H", cfg.RemoteDockerHost, "inspect", "--format", "{{.State.Running}}", containerID).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return false, err
		}
		return false, fmt.Errorf("%w: %s", err, message)
	}
	return strconv.ParseBool(strings.TrimSpace(string(output)))
}

func (a App) suspendStartedResources(sess *session.Session, syncs []startedSync, tunnels []ports.Tunnel) error {
	ctx, cancel := cleanupContext()
	defer cancel()
	if err := a.rollbackStartedResources(ctx, syncs, tunnels); err != nil {
		return err
	}
	deactivateSessionState(sess)
	a.saveSession(*sess)
	return nil
}

func cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func (a App) runComposeLifecycle(ctx context.Context, executor Executor, inv Invocation, composeArgs []string, action lifecycleAction) error {
	if action == lifecycleRestore {
		if err := a.restoreManagedSession(ctx, inv); err != nil {
			return err
		}
	}
	sess, ok, err := a.loadSessionForInvocation(inv)
	if err != nil {
		return err
	}
	args := composeRemoteArgs(inv, composeArgs, sess, ok)
	if err := executor.Run(ctx, Call{Name: inv.Config.RealDockerPath, Args: args}); err != nil {
		if action == lifecycleRestore {
			if cleanupErr := a.suspendManagedSession(ctx, inv); cleanupErr != nil {
				return fmt.Errorf("%w; restored session cleanup failed: %v", err, cleanupErr)
			}
		}
		return err
	}
	switch action {
	case lifecycleSuspend:
		return a.suspendManagedSession(ctx, inv)
	case lifecyclePurge:
		return a.purgeManagedSession(ctx, inv)
	default:
		return nil
	}
}

func composeRemoteArgs(inv Invocation, composeArgs []string, sess session.Session, hasSession bool) []string {
	if hasSession && len(sess.GeneratedFiles) > 0 && sess.GeneratedFiles[0] != "" {
		return append([]string{"-H", inv.Config.RemoteDockerHost, "compose", "-f", sess.GeneratedFiles[0]}, stripFileFlags(composeArgs)...)
	}
	return append([]string{"-H", inv.Config.RemoteDockerHost}, inv.Args...)
}

func (a App) loadSessionForInvocation(inv Invocation) (session.Session, bool, error) {
	if a.SessionStore == nil {
		return session.Session{}, false, nil
	}
	cwd := workingDir(inv.Cwd)
	projectDir := cwd
	if classification := cli.Classify(inv.Entrypoint, inv.Args); classification.Kind == cli.KindCompose {
		composeArgs := inv.Args
		if len(inv.Args) > 0 && inv.Args[0] == "compose" {
			composeArgs = inv.Args[1:]
		}
		if _, discoveredProjectDir, err := compose.DiscoverFiles(cwd, composeArgs); err == nil {
			projectDir = discoveredProjectDir
		}
	}
	id := session.Identity(projectDir, inv.Config.RemoteDockerHost, filepath.Base(projectDir))
	sess, err := a.SessionStore.Load(id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return session.Session{}, false, nil
		}
		return session.Session{}, false, err
	}
	return sess, true, nil
}

func (a App) suspendManagedSession(ctx context.Context, inv Invocation) error {
	sess, ok, err := a.loadSessionForInvocation(inv)
	if err != nil || !ok {
		return err
	}
	if err := a.deactivateManagedProcesses(ctx, inv.Config, &sess, false); err != nil {
		return err
	}
	return a.SessionStore.Save(sess)
}

func (a App) restoreManagedSession(ctx context.Context, inv Invocation) error {
	sess, ok, err := a.loadSessionForInvocation(inv)
	if err != nil || !ok {
		return err
	}
	if err := a.restoreSyncs(ctx, inv.Config, &sess); err != nil {
		return err
	}
	if err := a.restoreTunnels(ctx, inv.Config, &sess); err != nil {
		return err
	}
	return a.SessionStore.Save(sess)
}

func (a App) purgeManagedSession(ctx context.Context, inv Invocation) error {
	sess, ok, err := a.loadSessionForInvocation(inv)
	if err != nil || !ok {
		return err
	}
	if err := a.deactivateManagedProcesses(ctx, inv.Config, &sess, false); err != nil {
		return err
	}
	if err := a.SessionStore.Save(sess); err != nil {
		return err
	}
	cleaner := a.RemoteCleaner
	if cleaner == nil {
		cleaner = SSHRemoteWorkspaceCleaner{}
	}
	if err := cleaner.Remove(ctx, inv.Config, sess.RemoteWorkspace); err != nil {
		return err
	}
	for _, generated := range sess.GeneratedFiles {
		if generated == "" {
			continue
		}
		if err := os.Remove(generated); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return a.SessionStore.Delete(sess.ID)
}

func (a App) deactivateManagedProcesses(ctx context.Context, cfg config.Config, sess *session.Session, tolerateMissingMutagen bool) error {
	for i := range sess.Tunnels {
		if sess.Tunnels[i].Active {
			a.stopTunnels([]ports.Tunnel{{
				ID:     sess.Tunnels[i].ID,
				Active: sess.Tunnels[i].Active,
				PID:    sess.Tunnels[i].PID,
				Spec: ports.TunnelSpec{
					SessionID:  sess.ID,
					LocalBind:  sess.Tunnels[i].LocalBind,
					LocalPort:  sess.Tunnels[i].LocalPort,
					RemotePort: sess.Tunnels[i].RemotePort,
				},
			}})
			sess.Tunnels[i].Active = false
			sess.Tunnels[i].PID = 0
		}
	}
	for i := range sess.Syncs {
		if sess.Syncs[i].Backend == "mutagen" && sess.Syncs[i].Active && sess.Syncs[i].MutagenName != "" {
			if err := a.terminateMutagenSync(ctx, cfg.MutagenPath, sess.Syncs[i].MutagenName); err != nil {
				if tolerateMissingMutagen && isMissingMutagenSyncError(err) {
					sess.Syncs[i].Active = false
					continue
				}
				return err
			}
		}
		sess.Syncs[i].Active = false
	}
	return nil
}

func (a App) runNativeSessions(ctx context.Context, inv Invocation) error {
	if len(inv.Args) >= 2 && isHelpArg(inv.Args[1]) {
		return a.printSessionsHelp()
	}
	if len(inv.Args) >= 3 && isHelpArg(inv.Args[2]) {
		switch inv.Args[1] {
		case "cleanup", "list":
			return a.printSessionsHelp()
		}
	}
	if a.SessionStore == nil {
		return errors.New("session store is required")
	}
	if len(inv.Args) == 1 || inv.Args[1] == "list" {
		return a.listSessions()
	}
	if inv.Args[1] == "cleanup" {
		return a.cleanupSessions(ctx, inv.Config)
	}
	return fmt.Errorf("unsupported dockerbridge sessions command %q", inv.Args[1])
}

func (a App) printSessionsHelp() error {
	_, err := fmt.Fprint(a.output(), `Usage:
  dockerbridge sessions
  dockerbridge sessions list
  dockerbridge sessions cleanup

Commands:
  list      Show tracked DockBridge sessions with sync and tunnel counts.
  cleanup   Stop tracked tunnels and Mutagen syncs, remove remote caches, and delete session state.
`)
	return err
}

func (a App) listSessions() error {
	sessions, err := a.SessionStore.List()
	if err != nil {
		return err
	}
	out := a.output()
	if len(sessions) == 0 {
		_, err := fmt.Fprintln(out, "No DockBridge sessions.")
		return err
	}
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "ID\tLOCAL ROOT\tREMOTE\tWORKSPACE\tSYNCS active/total\tTUNNELS active/total\tUPDATED"); err != nil {
		return err
	}
	for _, sess := range sessions {
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			sess.ID,
			sess.LocalRoot,
			sess.RemoteTarget,
			sess.RemoteWorkspace,
			syncSummary(sess.Syncs),
			tunnelSummary(sess.Tunnels),
			formatUpdatedAt(sess.UpdatedAt),
		); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func (a App) cleanupSessions(ctx context.Context, cfg config.Config) error {
	sessions, err := a.SessionStore.List()
	if err != nil {
		return err
	}
	for _, sess := range sessions {
		cleanupCfg := cfg
		if sess.RemoteTarget != "" {
			cleanupCfg.RemoteDockerHost = sess.RemoteTarget
		}
		if err := a.cleanupSession(ctx, cleanupCfg, sess); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(a.output(), "Cleaned %d DockBridge session(s).\n", len(sessions))
	return err
}

func (a App) cleanupSession(ctx context.Context, cfg config.Config, sess session.Session) error {
	if err := a.deactivateManagedProcesses(ctx, cfg, &sess, true); err != nil {
		return err
	}
	if err := a.SessionStore.Save(sess); err != nil {
		return err
	}
	cleaner := a.RemoteCleaner
	if cleaner == nil {
		cleaner = SSHRemoteWorkspaceCleaner{}
	}
	if sess.RemoteWorkspace != "" {
		if err := cleaner.Remove(ctx, cfg, sess.RemoteWorkspace); err != nil {
			return err
		}
	}
	for _, generated := range sess.GeneratedFiles {
		if generated == "" {
			continue
		}
		if err := os.Remove(generated); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return a.SessionStore.Delete(sess.ID)
}

func (a App) restoreSyncs(ctx context.Context, cfg config.Config, sess *session.Session) error {
	if a.SyncDriver == nil {
		return nil
	}
	for i := range sess.Syncs {
		if sess.Syncs[i].Active || sess.Syncs[i].LocalPath == "" || sess.Syncs[i].RemotePath == "" {
			continue
		}
		syncSession, err := a.SyncDriver.Start(ctx, dbsync.Projection{
			SessionID:  sess.ID,
			LocalPath:  sess.Syncs[i].LocalPath,
			RemotePath: sess.Syncs[i].RemotePath,
			RemoteHost: cfg.RemoteDockerHost,
		})
		if err != nil {
			return err
		}
		status := syncSession.Status()
		sess.Syncs[i].ID = status.ID
		sess.Syncs[i].Active = status.Active
		sess.Syncs[i].Backend = status.Backend
		sess.Syncs[i].MutagenName = status.MutagenName
		sess.Syncs[i].MutagenIdentifier = status.MutagenIdentifier
		sess.Syncs[i].RemoteEndpoint = status.RemoteEndpoint
		sess.Syncs[i].LastStatus = status.LastStatus
	}
	return nil
}

func (a App) restoreTunnels(ctx context.Context, cfg config.Config, sess *session.Session) error {
	if a.TunnelStarter == nil {
		return nil
	}
	for i := range sess.Tunnels {
		if sess.Tunnels[i].Active || sess.Tunnels[i].LocalPort == 0 || sess.Tunnels[i].RemotePort == 0 {
			continue
		}
		tunnel, err := a.TunnelStarter.Start(ctx, ports.TunnelSpec{
			SessionID:  sess.ID,
			LocalBind:  sess.Tunnels[i].LocalBind,
			LocalPort:  sess.Tunnels[i].LocalPort,
			RemoteHost: "127.0.0.1",
			RemotePort: sess.Tunnels[i].RemotePort,
			SSHTarget:  cfg.RemoteDockerHost,
		})
		if err != nil {
			return err
		}
		sess.Tunnels[i].ID = tunnel.ID
		sess.Tunnels[i].Active = tunnel.Active
		sess.Tunnels[i].PID = tunnel.PID
	}
	return nil
}

func terminateMutagenSync(ctx context.Context, binary, name string) error {
	if binary == "" {
		binary = "mutagen"
	}
	output, err := exec.CommandContext(ctx, binary, "sync", "terminate", name).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, message)
	}
	return nil
}

func (a App) terminateMutagenSync(ctx context.Context, binary, name string) error {
	if a.MutagenTerminator != nil {
		return a.MutagenTerminator(ctx, binary, name)
	}
	return terminateMutagenSync(ctx, binary, name)
}

func isMissingMutagenSyncError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"not found",
		"not exist",
		"doesn't exist",
		"unknown synchronization session",
		"unable to locate",
		"does not correspond",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (a App) saveSession(sess session.Session) {
	if a.SessionStore != nil {
		_ = a.SessionStore.Save(sess)
	}
}

func (a App) output() io.Writer {
	if a.Output != nil {
		return a.Output
	}
	return os.Stdout
}

func syncStates(syncs []startedSync) []session.SyncState {
	states := make([]session.SyncState, 0, len(syncs))
	for _, sync := range syncs {
		states = append(states, sync.state)
	}
	return states
}

func deactivateSessionState(sess *session.Session) {
	for i := range sess.Syncs {
		sess.Syncs[i].Active = false
	}
	for i := range sess.Tunnels {
		sess.Tunnels[i].Active = false
		sess.Tunnels[i].PID = 0
	}
}

func isNativeEntrypoint(entrypoint string) bool {
	return entrypoint == "dockerbridge"
}

func isHelpArg(arg string) bool {
	return arg == "-h" || arg == "--help" || arg == "help"
}

func syncSummary(syncs []session.SyncState) string {
	active := 0
	for _, sync := range syncs {
		if sync.Active {
			active++
		}
	}
	return fmt.Sprintf("%d/%d", active, len(syncs))
}

func tunnelSummary(tunnels []session.TunnelState) string {
	active := 0
	for _, tunnel := range tunnels {
		if tunnel.Active {
			active++
		}
	}
	return fmt.Sprintf("%d/%d", active, len(tunnels))
}

func formatUpdatedAt(updated time.Time) string {
	if updated.IsZero() {
		return "-"
	}
	return updated.UTC().Format(time.RFC3339)
}

func (a App) isPortAvailable(bind string, port int) bool {
	if a.PortAvailable != nil {
		return a.PortAvailable(bind, port)
	}
	return ports.IsAvailable(bind, port)
}

func (a App) remotePortStart() int {
	if a.RemotePortStart != 0 {
		return a.RemotePortStart
	}
	return 49152
}

func workingDir(cwd string) string {
	if cwd != "" {
		return cwd
	}
	got, err := os.Getwd()
	if err != nil {
		return "."
	}
	return got
}

type lifecycleAction string

const (
	lifecycleNone    lifecycleAction = ""
	lifecycleSuspend lifecycleAction = "suspend"
	lifecycleRestore lifecycleAction = "restore"
	lifecyclePurge   lifecycleAction = "purge"
)

func shouldTranslateCompose(args []string) bool {
	for _, arg := range args {
		if arg == "up" || arg == "run" || arg == "build" {
			return true
		}
	}
	return false
}

func shouldRestoreBeforeDockerCommand(args []string) bool {
	return dockerLifecycleAction(args) == lifecycleRestore
}

func dockerLifecycleAction(args []string) lifecycleAction {
	if len(args) == 0 {
		return lifecycleNone
	}
	switch args[0] {
	case "stop":
		return lifecycleSuspend
	case "start":
		return lifecycleRestore
	case "rm":
		return lifecyclePurge
	default:
		return lifecycleNone
	}
}

func composeLifecycleAction(args []string) lifecycleAction {
	switch composeCommand(args) {
	case "stop":
		return lifecycleSuspend
	case "start":
		return lifecycleRestore
	case "rm", "down":
		return lifecyclePurge
	default:
		return lifecycleNone
	}
}

func composeCommand(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-f" || arg == "--file" || arg == "-p" || arg == "--project-name" || arg == "--project-directory" || arg == "--env-file" || arg == "--profile" {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-f=") || strings.HasPrefix(arg, "--file=") || strings.HasPrefix(arg, "-p=") || strings.HasPrefix(arg, "--project-name=") || strings.HasPrefix(arg, "--project-directory=") || strings.HasPrefix(arg, "--env-file=") || strings.HasPrefix(arg, "--profile=") {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return arg
	}
	return ""
}

func shouldCleanupAfterDockerCommand(args []string) bool {
	switch dockerLifecycleAction(args) {
	case lifecycleSuspend, lifecyclePurge:
		return true
	default:
		return false
	}
}

func isDetachedDockerRun(args []string) bool {
	for _, arg := range args {
		if arg == "-d" || arg == "--detach" {
			return true
		}
	}
	return false
}

func isInteractiveDockerRun(args []string) bool {
	for _, arg := range args {
		if arg == "-i" || arg == "--interactive" || arg == "-t" || arg == "--tty" {
			return true
		}
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.Contains(arg, "i") && strings.Contains(arg, "t") {
			return true
		}
	}
	return false
}

func isDetachedCompose(args []string) bool {
	for i, arg := range args {
		if arg == "-d" || arg == "--detach" {
			return true
		}
		if arg == "up" {
			for _, rest := range args[i+1:] {
				if rest == "-d" || rest == "--detach" {
					return true
				}
			}
		}
	}
	return false
}

func shouldCleanupDockerRunAfterExit(args []string, interrupted bool) bool {
	if isDetachedDockerRun(args) {
		return false
	}
	if isInteractiveDockerRun(args) {
		return interrupted
	}
	return true
}

func shouldWatchDockerRunCleanup(args []string) bool {
	return !isDetachedDockerRun(args)
}

func shouldCleanupComposeAfterExit(args []string) bool {
	return !isDetachedCompose(args)
}

func stripFileFlags(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-f" || arg == "--file" {
			i++
			continue
		}
		if len(arg) > 3 && (arg[:3] == "-f=") {
			continue
		}
		if len(arg) > 7 && arg[:7] == "--file=" {
			continue
		}
		out = append(out, arg)
	}
	return out
}

type SSHRemoteWorkspaceCleaner struct{}

func (SSHRemoteWorkspaceCleaner) Remove(ctx context.Context, cfg config.Config, remoteWorkspace string) error {
	if err := validateRemoteWorkspacePath(cfg.RemoteWorkspaceRoot, remoteWorkspace); err != nil {
		return err
	}
	target, port, err := sshTarget(cfg.RemoteDockerHost)
	if err != nil {
		return err
	}
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=5"}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, target, "rm -rf "+quoteRemoteShellArg(remoteWorkspace))
	output, err := exec.CommandContext(ctx, "ssh", args...).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, message)
	}
	return nil
}

func quoteRemoteShellArg(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func validateRemoteWorkspacePath(remoteRoot, remoteWorkspace string) error {
	root := path.Clean(remoteRoot)
	workspace := path.Clean(remoteWorkspace)
	if root == "." || root == "/" || workspace == "." || workspace == "/" {
		return fmt.Errorf("unsafe remote workspace cleanup path %q under root %q", remoteWorkspace, remoteRoot)
	}
	if !path.IsAbs(root) || !path.IsAbs(workspace) {
		return fmt.Errorf("remote workspace cleanup paths must be absolute: workspace %q root %q", remoteWorkspace, remoteRoot)
	}
	if workspace == root || !strings.HasPrefix(workspace+"/", root+"/") {
		return fmt.Errorf("remote workspace cleanup path %q is outside root %q", remoteWorkspace, remoteRoot)
	}
	return nil
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

type DockerValidator struct {
	Executor Executor
}

func (v DockerValidator) Validate(ctx context.Context, cfg config.Config) error {
	executor := v.Executor
	if executor == nil {
		cmd := exec.CommandContext(ctx, cfg.RealDockerPath, "-H", cfg.RemoteDockerHost, "version", "--format", "{{.Server.Version}}")
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		return cmd.Run()
	}
	return executor.Run(ctx, Call{Name: cfg.RealDockerPath, Args: []string{"-H", cfg.RemoteDockerHost, "version", "--format", "{{.Server.Version}}"}})
}

func EnvMap() map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		for i, ch := range kv {
			if ch == '=' {
				out[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	return out
}

func tunnelStates(tunnels []ports.Tunnel) []session.TunnelState {
	states := make([]session.TunnelState, 0, len(tunnels))
	for _, tunnel := range tunnels {
		states = append(states, session.TunnelState{
			ID:         tunnel.ID,
			LocalBind:  tunnel.Spec.LocalBind,
			LocalPort:  tunnel.Spec.LocalPort,
			RemotePort: tunnel.Spec.RemotePort,
			Active:     tunnel.Active,
			PID:        tunnel.PID,
		})
	}
	return states
}

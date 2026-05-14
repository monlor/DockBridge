package command

import (
	"context"
	"fmt"
	"io"
	"os"

	"dockbridge/internal/app"
	"dockbridge/internal/config"

	urfavecli "github.com/urfave/cli/v3"
)

var defaultVersion = "dev"

type ConfigLoader func(config.Options) (config.Config, error)

type EnvProvider func() map[string]string

type WorkingDirProvider func() (string, error)

type Options struct {
	Version        string
	LoadConfig     ConfigLoader
	NewRuntime     RuntimeFactory
	Env            EnvProvider
	WorkingDir     WorkingDirProvider
	Writer         io.Writer
	ErrWriter      io.Writer
	CompletionName string
}

func Run(ctx context.Context, args []string, opts Options) error {
	opts = opts.withDefaults()
	if len(args) == 0 {
		args = []string{"dockerbridge"}
	}
	if shouldDispatchDocker(args[1:]) {
		return runRuntime(ctx, args[1:], opts)
	}
	return New(opts).Run(ctx, args)
}

func New(opts Options) *urfavecli.Command {
	opts = opts.withDefaults()
	return &urfavecli.Command{
		Name:                       "dockerbridge",
		Usage:                      "Run Docker commands against a remote Docker host with local project semantics",
		Version:                    opts.Version,
		EnableShellCompletion:      true,
		ShellCompletionCommandName: opts.CompletionName,
		Writer:                     opts.Writer,
		ErrWriter:                  opts.ErrWriter,
		Commands: []*urfavecli.Command{
			sessionsCommand(opts),
		},
		Action: func(context.Context, *urfavecli.Command) error {
			_, err := fmt.Fprintln(opts.Writer, "Run 'dockerbridge --help' for usage.")
			return err
		},
	}
}

func sessionsCommand(opts Options) *urfavecli.Command {
	stopOnFirstArg := 0
	return &urfavecli.Command{
		Name:         "sessions",
		Usage:        "List or clean up tracked DockBridge sessions",
		ArgsUsage:    "[list|cleanup]",
		Description:  "Shows or cleans up DockBridge-managed sync sessions, tunnels, and remote workspaces.",
		StopOnNthArg: &stopOnFirstArg,
		Action: func(ctx context.Context, cmd *urfavecli.Command) error {
			args := append([]string{"sessions"}, cmd.Args().Slice()...)
			return runRuntime(ctx, args, opts)
		},
	}
}

func runRuntime(ctx context.Context, args []string, opts Options) error {
	cfg, err := opts.LoadConfig(config.Options{})
	if err != nil {
		return err
	}
	cwd, err := opts.WorkingDir()
	if err != nil {
		return err
	}
	return opts.NewRuntime(cfg).Run(ctx, app.Invocation{
		Entrypoint: "dockerbridge",
		Args:       append([]string(nil), args...),
		Env:        opts.Env(),
		Config:     cfg,
		Cwd:        cwd,
	})
}

func shouldDispatchDocker(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "-h", "--help", "help", "-v", "--version", "completion", "sessions":
		return false
	}
	return true
}

func (opts Options) withDefaults() Options {
	if opts.Version == "" {
		opts.Version = defaultVersion
	}
	if opts.LoadConfig == nil {
		opts.LoadConfig = config.Load
	}
	if opts.NewRuntime == nil {
		opts.NewRuntime = NewRuntime
	}
	if opts.Env == nil {
		opts.Env = app.EnvMap
	}
	if opts.WorkingDir == nil {
		opts.WorkingDir = os.Getwd
	}
	if opts.Writer == nil {
		opts.Writer = os.Stdout
	}
	if opts.ErrWriter == nil {
		opts.ErrWriter = os.Stderr
	}
	if opts.CompletionName == "" {
		opts.CompletionName = "completion"
	}
	return opts
}

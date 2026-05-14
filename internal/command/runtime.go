package command

import (
	"context"

	"dockbridge/internal/app"
	"dockbridge/internal/config"
	"dockbridge/internal/ports"
	"dockbridge/internal/session"
	dbsync "dockbridge/internal/sync"
)

type RuntimeFactory func(config.Config) Runner

type Runner interface {
	Run(context.Context, app.Invocation) error
}

func NewRuntime(cfg config.Config) Runner {
	return app.App{
		SyncDriver:      dbsync.MutagenDriver{Binary: cfg.MutagenPath, Mode: cfg.MutagenMode, Ignores: cfg.MutagenIgnores},
		TunnelStarter:   ports.SSHTunnelManager{},
		SessionStore:    session.NewStore(cfg.StateDir),
		RemoteCleaner:   app.SSHRemoteWorkspaceCleaner{},
		RemoteValidator: app.DockerValidator{},
		RemotePortStart: cfg.RemotePortStart,
	}
}

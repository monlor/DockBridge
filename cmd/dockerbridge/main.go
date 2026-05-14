package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"dockbridge/internal/command"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := command.Run(ctx, os.Args, command.Options{}); err != nil {
		fmt.Fprintln(os.Stderr, "dockerbridge:", err)
		os.Exit(1)
	}
}

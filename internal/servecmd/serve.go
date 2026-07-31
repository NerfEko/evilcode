// Package servecmd implements `evilcode serve`: the daemon of plan.md §20.
package servecmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"evilcode/internal/config"
	"evilcode/internal/daemon"
)

// Run starts the daemon and blocks until it is signalled.
func Run(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	model := fs.String("m", "", "default model reference for sessions this daemon creates")
	socket := fs.String("socket", "", "socket path (default $XDG_RUNTIME_DIR/evilcode.sock)")
	quiet := fs.Bool("q", false, "do not print the startup line")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	srv := daemon.NewServer(cfg, cwd, *model)
	if *socket != "" {
		srv.Path = *socket
	}
	if err := srv.Listen(); err != nil {
		return err
	}
	if !*quiet {
		fmt.Fprintf(os.Stderr, "evilcode serve: listening on %s\n", srv.Path)
	}

	// SIGTERM and SIGINT both mean stop, and stopping has to remove the socket
	// — a leftover socket file is what makes the next `serve` refuse to start.
	ctx, cancel := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer cancel()

	return srv.Serve(ctx)
}

// Usage prints the subcommand's flags.
func Usage() string {
	return "evilcode serve [-m model] [-socket path] [-q]"
}

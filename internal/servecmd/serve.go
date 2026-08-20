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
	idle := fs.Duration("idle", daemon.DefaultIdleTimeout, "shutdown after this long with no clients or running agents (0 disables)")
	status := fs.Bool("status", false, "print daemon status and exit")
	stop := fs.Bool("stop", false, "request a graceful daemon shutdown and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}

	path := *socket
	if path == "" {
		path = daemon.SocketPath()
	}
	if *status || *stop {
		client, err := daemon.DialPath(path)
		if err != nil {
			return err
		}
		defer client.Close()
		if *stop {
			if err := client.Stop(); err != nil {
				return err
			}
			fmt.Fprintln(os.Stdout, "evilcode server stopping")
			return nil
		}
		info, err := client.Status()
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "pid=%d socket=%s sessions=%d clients=%d running=%d idle=%s\n",
			info.PID, info.Socket, info.Sessions, info.Clients, info.Running, info.IdleTimeout)
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// Keep the daemon's shared config aware of a locally logged-in Codex
	// account; individual sessions clone this config before applying repo
	// overrides.
	cfg.AddDiscoveredCodex()
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	srv := daemon.NewServer(cfg, cwd, *model)
	srv.Path = path
	srv.IdleTimeout = *idle
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
	return "evilcode serve [-m model] [-socket path] [-idle duration] [-q] [-status|-stop]"
}

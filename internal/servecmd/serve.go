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
	web := fs.Bool("web", false, "serve the web UI (overrides [webui] enabled)")
	webAddr := fs.String("web-addr", "", "web UI bind address, host:port (overrides [webui] addr)")
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
		web := "-"
		if info.Web != "" {
			web = info.Web
		}
		fmt.Fprintf(os.Stdout, "pid=%d socket=%s sessions=%d clients=%d running=%d idle=%s web=%s\n",
			info.PID, info.Socket, info.Sessions, info.Clients, info.Running, info.IdleTimeout, web)
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

	// The web surface is opt-in: `[webui] enabled` in the config, or `-web`
	// for one run. `-web-addr` overrides `[webui] addr` either way (§10).
	webEnabled := *web || cfg.WebUI.Enabled
	webBind := cfg.WebUI.Addr
	if *webAddr != "" {
		webBind = *webAddr
	}

	srv := daemon.NewServer(cfg, cwd, *model)
	srv.Path = path
	srv.IdleTimeout = *idle
	if err := srv.Listen(); err != nil {
		return err
	}
	if webEnabled {
		// Independence (§2): a web bind failure must not take the socket down.
		// The user asked for the web UI, so the failure is loud, not swallowed.
		if err := srv.ListenWeb(webBind); err != nil {
			fmt.Fprintf(os.Stderr, "evilcode: web UI unavailable: %v\n", err)
		} else if info := srv.WebInfo(); info != nil {
			if !info.RequireAuth {
				fmt.Fprintf(os.Stderr, "evilcode web: http://%s (authentication disabled)\n", info.Addr)
			} else {
				fmt.Fprintf(os.Stderr, "evilcode web: http://%s (token: %s)\n", info.Addr, info.TokenPath)
				// The full tokenized URL is printed exactly once — at first mint
				// (§3). Every later start names the file instead; deleting the
				// file rotates the token and reprints the URL on the next start.
				if info.Minted {
					fmt.Fprintf(os.Stderr, "evilcode web: open http://%s/?token=%s once to hand the token to your browser\n", info.Addr, info.Token)
				}
			}
		}
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
	return "evilcode serve [-m model] [-socket path] [-idle duration] [-web] [-web-addr host:port] [-q] [-status|-stop]"
}

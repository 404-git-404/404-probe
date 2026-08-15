package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"text/tabwriter"
	"time"

	"404-probe/internal/auth"
	appserver "404-probe/internal/server"
	"404-probe/internal/storage"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("server command failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "agent":
		return agentCommand(args[1:])
	case "help", "-h", "--help":
		return usageError()
	default:
		return fmt.Errorf("unknown command %q\n%w", args[0], usageError())
	}
}

func usageError() error {
	return errors.New("usage: 404-probe-server serve [flags] | agent add <name> [--db path] | agent list [--db path] | agent revoke <id> [--db path]")
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := flags.String("listen", ":8080", "listen address")
	dbPath := flags.String("db", "404-probe.db", "SQLite database path")
	offline := flags.Duration("offline-timeout", 30*time.Second, "offline threshold")
	if err := flags.Parse(args); err != nil {
		return err
	}
	store, err := openStore(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	app, err := appserver.NewApp(store, *offline, slog.Default())
	if err != nil {
		return err
	}
	httpServer := &http.Server{Addr: *listen, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		app.CleanupLoop()
	}()
	defer func() {
		app.Shutdown()
		<-cleanupDone
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		slog.Info("server listening", "address", *listen, "database", *dbPath)
		errCh <- httpServer.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		app.Shutdown()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
}

func agentCommand(args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	sub := args[0]
	flags := flag.NewFlagSet("agent "+sub, flag.ContinueOnError)
	dbPath := flags.String("db", "404-probe.db", "SQLite database path")
	positional := args[1:]
	var value string
	if len(positional) > 0 && !startsFlag(positional[0]) {
		value = positional[0]
		positional = positional[1:]
	}
	if err := flags.Parse(positional); err != nil {
		return err
	}
	store, err := openStore(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	ctx := context.Background()
	switch sub {
	case "add":
		if value == "" {
			return errors.New("agent name is required")
		}
		id, err := auth.NewID()
		if err != nil {
			return err
		}
		plain, hash, err := auth.NewToken()
		if err != nil {
			return err
		}
		if err = store.AddAgent(ctx, id, value, hash, time.Now()); err != nil {
			return err
		}
		fmt.Printf("Agent ID: %s\nToken: %s\n\nSave this token now; the server stores only its SHA-256 hash.\n", id, plain)
		return nil
	case "list":
		agents, err := store.ListAgents(ctx)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tREVOKED\tCREATED")
		for _, a := range agents {
			fmt.Fprintf(w, "%s\t%s\t%t\t%s\n", a.ID, a.Name, a.Revoked, time.UnixMilli(a.CreatedAt).Format(time.RFC3339))
		}
		return w.Flush()
	case "revoke":
		if value == "" {
			return errors.New("agent ID is required")
		}
		changed, err := store.RevokeAgent(ctx, value, time.Now())
		if err != nil {
			return err
		}
		if !changed {
			return errors.New("agent not found or already revoked")
		}
		fmt.Printf("Revoked agent %s\n", value)
		return nil
	default:
		return usageError()
	}
}

func startsFlag(value string) bool { return len(value) > 0 && value[0] == '-' }
func openStore(path string) (*storage.Store, error) {
	if path != ":memory:" {
		dir := filepath.Dir(path)
		if dir != "." {
			if err := os.MkdirAll(dir, 0700); err != nil {
				return nil, err
			}
		}
	}
	store, err := storage.Open(context.Background(), path)
	if err != nil {
		return nil, err
	}
	if path != ":memory:" {
		if err := os.Chmod(path, 0600); err != nil {
			store.Close()
			return nil, err
		}
	}
	return store, nil
}

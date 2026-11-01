package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"tinnie/internal/app"
	"tinnie/internal/config"
	"tinnie/internal/proc"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := "help"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "version", "--version":
		fmt.Println(version)
		return nil
	case "help", "-h", "--help":
		return help()
	}

	cfg, origin, err := config.Load()
	if err != nil {
		return err
	}
	a, err := app.New(cfg, app.DefaultPaths())
	if err != nil {
		return err
	}
	// SIGHUP too: closing a console mid-backup must still run the deferred restarts of stopped services.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	switch cmd {
	case "run":
		proc.Logf("config loaded from %s", origin)
		return a.Run(ctx, version)
	case "backup":
		return a.Backup(ctx, 1)
	case "snapshots":
		if err := a.Repo.Ensure(ctx); err != nil {
			return err
		}
		return a.Repo.PrintSnapshots(ctx)
	case "restore":
		opts := app.RestoreOptions{}
		for _, arg := range args {
			switch {
			case arg == "--yes" || arg == "-y":
				opts.Yes = true
			case opts.Snapshot == "":
				opts.Snapshot = arg
			default:
				opts.Sources = append(opts.Sources, arg)
			}
		}
		return a.Restore(ctx, opts)
	case "check":
		return a.Check(ctx)
	case "health":
		if err := a.Health(time.Now()); err != nil {
			return errors.New("unhealthy: " + err.Error())
		}
		fmt.Println("healthy")
		return nil
	}
	return fmt.Errorf("unknown command %q; run tinnie help", cmd)
}

func help() error {
	cfg, _, err := config.Load()
	if err != nil {
		fmt.Printf("tinnie %s\n\n%v\n\n%s", version, err, app.Commands)
		return nil
	}
	a, err := app.New(cfg, app.DefaultPaths())
	if err != nil {
		return err
	}
	a.PrintSummary(os.Stdout)
	fmt.Print(app.Commands)
	return nil
}

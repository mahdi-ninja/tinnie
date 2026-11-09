// tinnie-server is the backup server for tinnie stacks: a restic rest-server in append-only mode with a
// private repository per stack, and the retention the stacks cannot run themselves.
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

	"tinnie/internal/config"
	"tinnie/internal/proc"
	"tinnie/internal/server"
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

	cfg, origin, err := config.LoadServer()
	if err != nil {
		return err
	}
	s := server.New(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	switch cmd {
	case "run":
		proc.Logf("config loaded from %s", origin)
		return s.Run(ctx, version)
	case "forget":
		return s.Forget(ctx, args)
	case "snapshots":
		if len(args) != 1 {
			return errors.New("usage: tinnie-server snapshots <stack>")
		}
		return s.Restic(ctx, args[0], []string{"snapshots"})
	case "check":
		return s.Check(ctx, func(format string, a ...any) { fmt.Printf(format+"\n", a...) })
	case "restic":
		if len(args) < 2 {
			return errors.New("usage: tinnie-server restic <stack> <restic arguments...>")
		}
		return s.Restic(ctx, args[0], args[1:])
	case "health":
		if err := s.Health(ctx, time.Now()); err != nil {
			return errors.New("unhealthy: " + err.Error())
		}
		fmt.Println("healthy")
		return nil
	}
	return fmt.Errorf("unknown command %q; run tinnie-server help", cmd)
}

func help() error {
	cfg, _, err := config.LoadServer()
	if err != nil {
		fmt.Printf("tinnie-server %s\n\n%v\n\n%s", version, err, server.Commands)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	server.New(cfg).PrintSummary(ctx, os.Stdout)
	fmt.Print(server.Commands)
	return nil
}

package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// RestServerArgs is the command line tinnie-server runs rest-server with. Append-only and private
// repositories are not optional: they are the point of the server.
func (s *Server) RestServerArgs() []string {
	args := []string{
		"--path", s.Data,
		"--listen", ":" + Port,
		"--htpasswd-file", s.Htpasswd(),
		"--append-only",
		"--private-repos",
	}
	if s.Cfg.MaxSize > 0 {
		args = append(args, "--max-size", strconv.FormatInt(s.Cfg.MaxSize, 10))
	}
	return args
}

// StartRestServer runs rest-server as a child process and returns a channel that gets its exit error (nil on a
// clean exit). Cancelling ctx sends it SIGTERM.
func (s *Server) StartRestServer(ctx context.Context) (<-chan error, error) {
	cmd := exec.CommandContext(ctx, "rest-server", s.RestServerArgs()...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("rest-server: %w", err)
	}
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		if ctx.Err() != nil {
			err = nil
		}
		done <- err
	}()
	return done, nil
}

// restServerURL is what the healthcheck probes; tests point it at a stand-in.
var restServerURL = "http://127.0.0.1:" + Port + "/"

// PingRestServer checks that rest-server answers on its port. Any HTTP response counts, including the 401 an
// unauthenticated request gets.
func PingRestServer(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, restServerURL, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("rest-server is not answering on port %s: %w", Port, err)
	}
	resp.Body.Close()
	return nil
}

// waitForRestServer polls until rest-server answers or the child exits.
func waitForRestServer(ctx context.Context, exited <-chan error) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := PingRestServer(ctx); err == nil {
			return nil
		}
		select {
		case err := <-exited:
			if err == nil {
				err = errors.New("rest-server exited")
			}
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return errors.New("rest-server did not start listening within 30 seconds")
		}
	}
}

package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"tinnie/internal/config"
	"tinnie/internal/docker"
)

const outputFile = "output"

// ErrNoRestore is returned by Restore for a source that has no way to restore what it backed up.
var ErrNoRestore = errors.New("no restore command configured")

// Restorable reports whether Restore can work for s, so a restore can skip or refuse it before changing anything.
func Restorable(s Source) bool {
	r, ok := s.(interface{ Restorable() bool })
	return !ok || r.Restorable()
}

// Docker is the part of the Docker client command sources use.
type Docker interface {
	RunningContainer(ctx context.Context, service string) (docker.Container, error)
	Exec(ctx context.Context, id string, opts docker.ExecOptions) (int, error)
}

// command runs shell commands in the container of a Compose service through Docker exec:
// the backup command's stdout is saved, and the restore command gets it back on stdin.
// The commands themselves never appear in summaries, logs or errors, since they may hold passwords.
type command struct {
	cfg    config.Command
	docker Docker
}

func (c *command) Name() string     { return c.cfg.Name }
func (c *command) Type() string     { return "command" }
func (c *command) Restorable() bool { return c.cfg.Restore != "" }

func (c *command) Describe() string {
	if c.cfg.Restore == "" {
		return "service " + c.cfg.Service + " (no restore)"
	}
	return "service " + c.cfg.Service
}

func (c *command) Check(ctx context.Context) error {
	_, err := c.docker.RunningContainer(ctx, c.cfg.Service)
	return err
}

func (c *command) Backup(ctx context.Context, dir string) error {
	tmp := filepath.Join(dir, outputFile+".partial")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	err = c.run(ctx, "backup", c.cfg.Backup, nil, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, outputFile))
}

func (c *command) Restore(ctx context.Context, dir string) error {
	if c.cfg.Restore == "" {
		return ErrNoRestore
	}
	f, err := os.Open(filepath.Join(dir, outputFile))
	if err != nil {
		return fmt.Errorf("snapshot has no command output for %s", c.cfg.Name)
	}
	defer f.Close()
	return c.run(ctx, "restore", c.cfg.Restore, f, os.Stdout)
}

// run runs the backup or restore command (which) with /bin/sh -c in the service's container. Its stderr is
// shown as it comes, and the last lines are kept for the error if it fails.
func (c *command) run(ctx context.Context, which, script string, stdin io.Reader, stdout io.Writer) error {
	ct, err := c.docker.RunningContainer(ctx, c.cfg.Service)
	if err != nil {
		return err
	}
	tail := &tailBuffer{max: 4096}
	code, err := c.docker.Exec(ctx, ct.ID, docker.ExecOptions{
		Cmd:    []string{"/bin/sh", "-c", script},
		User:   c.cfg.User,
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: io.MultiWriter(os.Stderr, tail),
	})
	if err != nil {
		return fmt.Errorf("the %s command in %s: %w", which, c.cfg.Service, err)
	}
	if code != 0 {
		msg := fmt.Sprintf("the %s command in %s exited with status %d", which, c.cfg.Service, code)
		if last := tail.lastLines(5); last != "" {
			msg += ": " + last
		}
		return errors.New(msg)
	}
	return nil
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) lastLines(n int) string {
	lines := strings.Split(strings.TrimSpace(string(t.buf)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

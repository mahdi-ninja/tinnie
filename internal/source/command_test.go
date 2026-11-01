package source

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tinnie/internal/config"
	"tinnie/internal/docker"
)

// fakeDocker runs nothing: each exec writes stderr and exits with code.
type fakeDocker struct {
	code   int
	stderr string
	err    error
}

func (f *fakeDocker) RunningContainer(ctx context.Context, service string) (docker.Container, error) {
	return docker.Container{ID: "c1", State: "running"}, nil
}

func (f *fakeDocker) Exec(ctx context.Context, id string, opts docker.ExecOptions) (int, error) {
	io.WriteString(opts.Stderr, f.stderr)
	return f.code, f.err
}

func TestCommandDescribe(t *testing.T) {
	c := &command{cfg: config.Command{Name: "g", Service: "gitea", Backup: "mysqldump -psecret db", Restore: "mysql -psecret db"}}
	if got := c.Describe(); got != "service gitea" {
		t.Errorf("Describe = %q", got)
	}
	c.cfg.Restore = ""
	if got := c.Describe(); got != "service gitea (no restore)" {
		t.Errorf("Describe without restore = %q", got)
	}
}

func TestCommandErrorsHideTheCommand(t *testing.T) {
	stderr := os.Stderr
	os.Stderr, _ = os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	defer func() { os.Stderr = stderr }()

	f := &fakeDocker{code: 2, stderr: "access denied\n"}
	c := &command{cfg: config.Command{Name: "g", Service: "gitea", Backup: "dump -psecret", Restore: "load -psecret"}, docker: f}
	dir := t.TempDir()
	err := c.Backup(context.Background(), dir)
	if err == nil || err.Error() != "the backup command in gitea exited with status 2: access denied" {
		t.Errorf("backup error = %v", err)
	}
	f.code, f.err = 0, errors.New("lost the connection to Docker while running the command: EOF")
	err = c.Backup(context.Background(), dir)
	if err == nil || strings.Contains(err.Error(), "secret") || !strings.HasPrefix(err.Error(), "the backup command in gitea: ") {
		t.Errorf("backup exec error = %v", err)
	}
	os.WriteFile(filepath.Join(dir, outputFile), []byte("x"), 0o600)
	f.code, f.err = 1, nil
	err = c.Restore(context.Background(), dir)
	if err == nil || err.Error() != "the restore command in gitea exited with status 1: access denied" {
		t.Errorf("restore error = %v", err)
	}
}

func TestCommandWithoutRestore(t *testing.T) {
	c := &command{cfg: config.Command{Name: "g", Service: "gitea", Backup: "true"}}
	if Restorable(c) {
		t.Error("command without restore is restorable")
	}
	if err := c.Restore(context.Background(), t.TempDir()); !errors.Is(err, ErrNoRestore) {
		t.Errorf("Restore = %v, want ErrNoRestore", err)
	}
	c.cfg.Restore = "cat"
	if !Restorable(c) || !Restorable(&volume{}) {
		t.Error("sources with a restore are not restorable")
	}
}

func TestVolumeCheck(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	link := filepath.Join(dir, "link")
	file := filepath.Join(dir, "file")
	os.Mkdir(real, 0o700)
	os.Symlink(real, link)
	os.WriteFile(file, nil, 0o600)
	for path, want := range map[string]string{
		real:                         "",
		link:                         link + " is a symlink; mount the volume directly at " + link,
		file:                         file + " is not a folder: mount the volume at " + file + " in the tinnie service",
		filepath.Join(dir, "absent"): "is not a folder",
	} {
		err := (&volume{cfg: config.Volume{Path: path}}).Check(context.Background())
		if (want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), want)) {
			t.Errorf("Check(%s) = %v, want %q", path, err, want)
		}
	}
}

func TestTailBuffer(t *testing.T) {
	tb := &tailBuffer{max: 16}
	tb.Write([]byte("line one\nline two\n"))
	tb.Write([]byte("line three\n"))
	if got := tb.lastLines(5); got != "two\nline three" {
		t.Errorf("tail = %q", got)
	}
	if got := tb.lastLines(1); got != "line three" {
		t.Errorf("last line = %q", got)
	}
}

package docker

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestLive runs against the local Docker daemon: TINNIE_DOCKER_TEST=1 go test ./internal/docker -run Live
func TestLive(t *testing.T) {
	if os.Getenv("TINNIE_DOCKER_TEST") == "" {
		t.Skip("set TINNIE_DOCKER_TEST=1 to run against the local Docker daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	project := "tinnielive" + time.Now().Format("150405")
	out, err := exec.Command("docker", "run", "-d",
		"--label", labelProject+"="+project, "--label", labelService+"=app",
		"alpine:3.23", "sleep", "infinity").Output()
	if err != nil {
		t.Fatalf("docker run: %v", err)
	}
	id := strings.TrimSpace(string(out))
	defer exec.Command("docker", "rm", "-f", id).Run()

	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	c.me = &Container{ID: "not-a-container", Labels: map[string]string{labelProject: project}} // tinnie is not in a container here

	ct, err := c.RunningContainer(ctx, "app")
	if err != nil || ct.ID != id {
		t.Fatalf("running container = %+v, %v", ct, err)
	}

	// stdin round trip of a few MB, with stderr kept apart.
	payload := bytes.Repeat([]byte("tinnie\x00\xff"), 512*1024)
	var stdout, stderr bytes.Buffer
	code, err := c.Exec(ctx, id, ExecOptions{
		Cmd:   []string{"/bin/sh", "-c", "echo starting >&2; cat; echo done >&2"},
		Stdin: bytes.NewReader(payload), Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil || code != 0 {
		t.Fatalf("exec = %d, %v (stderr %q)", code, err, stderr.String())
	}
	if !bytes.Equal(stdout.Bytes(), payload) || stderr.String() != "starting\ndone\n" {
		t.Errorf("stdout %d bytes (want %d), stderr %q", stdout.Len(), len(payload), stderr.String())
	}

	stdout.Reset()
	code, err = c.Exec(ctx, id, ExecOptions{Cmd: []string{"/bin/sh", "-c", "id -un; exit 3"}, User: "nobody", Stdout: &stdout})
	if err != nil || code != 3 || stdout.String() != "nobody\n" {
		t.Errorf("exec as nobody = %d, %v, %q", code, err, stdout.String())
	}

	// A command that ignores its input must not hang or fail the exec.
	code, err = c.Exec(ctx, id, ExecOptions{Cmd: []string{"/bin/sh", "-c", "true"}, Stdin: bytes.NewReader(payload)})
	if err != nil || code != 0 {
		t.Errorf("exec ignoring stdin = %d, %v", code, err)
	}

	if err := c.Stop(ctx, id, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if ct, err := c.Inspect(ctx, id); err != nil || ct.Running() {
		t.Errorf("after stop: %+v, %v", ct, err)
	}
	if _, err := c.RunningContainer(ctx, "app"); err == nil || !strings.Contains(err.Error(), "no running container for service \"app\"") {
		t.Errorf("stopped service: %v", err)
	}
	if err := c.Stop(ctx, id, time.Second); err != nil {
		t.Errorf("stopping a stopped container: %v", err)
	}
	if err := c.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(ctx, id); err != nil {
		t.Errorf("starting a running container: %v", err)
	}
	if ct, err := c.Inspect(ctx, id); err != nil || !ct.Running() {
		t.Errorf("after start: %+v, %v", ct, err)
	}
}

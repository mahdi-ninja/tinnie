package restic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"

	"tinnie/internal/config"
	"tinnie/internal/proc"
)

const exitRepositoryMissing = 10

type Repo struct {
	URL      string
	Password string
	Env      map[string]string
	Host     string
	CacheDir string
}

type Snapshot struct {
	ID      string    `json:"id"`
	ShortID string    `json:"short_id"`
	Time    time.Time `json:"time"`
}

type Summary struct {
	SnapshotID   string `json:"snapshot_id"`
	FilesNew     int    `json:"files_new"`
	FilesChanged int    `json:"files_changed"`
	DataAdded    int64  `json:"data_added"`
	TotalBytes   int64  `json:"total_bytes_processed"`
}

func (r Repo) env() map[string]string {
	env := map[string]string{
		"RESTIC_REPOSITORY": r.URL,
		"RESTIC_PASSWORD":   r.Password,
	}
	if r.CacheDir != "" {
		env["RESTIC_CACHE_DIR"] = r.CacheDir
	}
	for k, v := range r.Env {
		env[k] = v
	}
	return env
}

func (r Repo) cmd(ctx context.Context, args ...string) *exec.Cmd {
	return proc.Command(ctx, r.env(), "restic", args...)
}

func (r Repo) Ensure(ctx context.Context) error {
	err := proc.RunQuiet(ctx, r.env(), "restic", "cat", "config")
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == exitRepositoryMissing {
		proc.Logf("creating restic repository at %s", Redact(r.URL))
		return proc.RunQuiet(ctx, r.env(), "restic", "init")
	}
	return err
}

// Backup saves every path in one snapshot. Excludes are restic patterns, normally absolute.
func (r Repo) Backup(ctx context.Context, paths, excludes []string) (Summary, error) {
	args := []string{"backup", "--json", "--host", r.Host, "--tag", "tinnie"}
	for _, ex := range excludes {
		args = append(args, "--exclude", ex)
	}
	args = append(args, "--")
	cmd := r.cmd(ctx, append(args, paths...)...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	var summary Summary
	scanner := bufio.NewScanner(&stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var msg struct {
			Type    string `json:"message_type"`
			Item    string `json:"item"`
			Message string `json:"message"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			continue
		}
		switch msg.Type {
		case "summary":
			_ = json.Unmarshal(scanner.Bytes(), &summary)
		case "error":
			proc.Logf("restic: %s: %s", msg.Item, msg.Error.Message)
		case "exit_error":
			proc.Logf("restic: %s", msg.Message)
		}
	}
	if err != nil {
		return summary, fmt.Errorf("restic backup: %w", err)
	}
	return summary, nil
}

func (r Repo) Forget(ctx context.Context, keep config.Keep) error {
	args := []string{"forget", "--host", r.Host, "--prune"}
	add := func(flag string, n int) {
		if n > 0 {
			args = append(args, flag, strconv.Itoa(n))
		}
	}
	if keep.Within != "" {
		args = append(args, "--keep-within", keep.Within)
	}
	add("--keep-last", keep.Last)
	add("--keep-hourly", keep.Hourly)
	add("--keep-daily", keep.Daily)
	add("--keep-weekly", keep.Weekly)
	add("--keep-monthly", keep.Monthly)
	add("--keep-yearly", keep.Yearly)
	return proc.RunQuiet(ctx, r.env(), "restic", args...)
}

func (r Repo) PrintSnapshots(ctx context.Context) error {
	return proc.Run(ctx, r.env(), "restic", "snapshots", "--host", r.Host)
}

func (r Repo) Latest(ctx context.Context) (*Snapshot, error) {
	cmd := r.cmd(ctx, "snapshots", "--json", "--latest", "1", "--host", r.Host)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == exitRepositoryMissing {
			return nil, nil
		}
		return nil, fmt.Errorf("restic snapshots: %w: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	var snaps []Snapshot
	if err := json.Unmarshal(stdout.Bytes(), &snaps); err != nil {
		return nil, fmt.Errorf("restic snapshots: %w", err)
	}
	if len(snaps) == 0 {
		return nil, nil
	}
	return &snaps[len(snaps)-1], nil
}

// Node is an entry in a snapshot, as restic ls --json prints it.
type Node struct {
	Path string      `json:"path"`
	Type string      `json:"type"`
	UID  int         `json:"uid"`
	GID  int         `json:"gid"`
	Mode os.FileMode `json:"mode"`
}

// Lookup resolves snapshot (an ID or "latest") and returns its full ID along with the entries found at paths.
// Paths missing from the snapshot are simply absent from the map.
func (r Repo) Lookup(ctx context.Context, snapshot string, paths []string) (string, map[string]Node, error) {
	args := append([]string{"ls", "--json", "--host", r.Host, snapshot, "--"}, paths...)
	cmd := r.cmd(ctx, args...)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = nil, &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", nil, err
	}
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("restic ls: %w", err)
	}
	want := map[string]bool{}
	for _, p := range paths {
		want[p] = true
	}
	var id string
	found := map[string]Node{}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var msg struct {
			Type string `json:"message_type"`
			ID   string `json:"id"`
			Node
		}
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			continue
		}
		switch {
		case msg.Type == "snapshot":
			id = msg.ID
		case msg.Type == "node" && want[msg.Path]:
			found[msg.Path] = msg.Node
		}
	}
	io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil {
		msg := bytes.TrimSpace(stderr.Bytes())
		var exit struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(msg, &exit) == nil && exit.Message != "" {
			msg = []byte(exit.Message)
		}
		return "", nil, fmt.Errorf("restic ls: %w: %s", err, msg)
	}
	if id == "" {
		return "", nil, fmt.Errorf("restic ls: snapshot %s not found", snapshot)
	}
	return id, found, nil
}

// RestoreInPlace makes dir match its copy in the snapshot: changed files are rewritten and files added since
// are deleted. Paths matching excludes (relative to dir) are left alone. The folder's own owner and mode are
// set from the snapshot too, since restic only restores what is inside it.
func (r Repo) RestoreInPlace(ctx context.Context, snapshot string, dir Node, excludes []string) error {
	args := []string{"restore", snapshot + ":" + dir.Path, "--host", r.Host, "--target", dir.Path, "--delete"}
	for _, ex := range excludes {
		args = append(args, "--exclude", "/"+ex)
	}
	if err := proc.Run(ctx, r.env(), "restic", args...); err != nil {
		return err
	}
	if err := os.Lchown(dir.Path, dir.UID, dir.GID); err != nil {
		return err
	}
	return os.Chmod(dir.Path, dir.Mode)
}

func (r Repo) Restore(ctx context.Context, snapshot, target string, includes []string) error {
	args := []string{"restore", snapshot, "--host", r.Host, "--target", target}
	for _, inc := range includes {
		args = append(args, "--include", inc)
	}
	return proc.Run(ctx, r.env(), "restic", args...)
}

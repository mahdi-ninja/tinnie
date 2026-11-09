package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tinnie/internal/config"
)

func newTest(t *testing.T, text string) *Server {
	t.Helper()
	cfg, err := config.ParseServer([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TINNIE_SERVER_DATA", t.TempDir())
	return New(cfg)
}

const twoStacks = `
retention:
  schedule: "0 3 * * *"
  keep: { last: 1 }
stacks:
  - { name: mysite, password: rest-a, backup_password: backup-a }
  - { name: shop, password: rest-b }
`

func TestRestServerArgs(t *testing.T) {
	s := newTest(t, "max_size: 1G\n"+twoStacks)
	got := strings.Join(s.RestServerArgs(), " ")
	for _, want := range []string{"--path " + s.Data, "--listen :8000", "--htpasswd-file " + s.Data + "/.tinnie-server/htpasswd", "--append-only", "--private-repos", "--max-size 1073741824"} {
		if !strings.Contains(got, want) {
			t.Errorf("args %q lack %q", got, want)
		}
	}
	s = newTest(t, twoStacks)
	if strings.Contains(strings.Join(s.RestServerArgs(), " "), "--max-size") {
		t.Error("max-size set without max_size")
	}
}

func TestHealth(t *testing.T) {
	s := newTest(t, twoStacks)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

	restServerURL = "http://127.0.0.1:1/"
	if err := s.Health(ctx, now); err == nil || !strings.Contains(err.Error(), "not answering") {
		t.Errorf("without rest-server: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer srv.Close()
	restServerURL = srv.URL + "/"

	if err := s.Health(ctx, now); err != nil {
		t.Errorf("before the first run: %v", err)
	}
	ok := now.Add(-6 * time.Hour)
	s.WriteStatus(Status{LastAttempt: ok, LastResult: "success", LastSuccess: ok})
	if err := s.Health(ctx, now); err != nil {
		t.Errorf("after a success: %v", err)
	}
	s.WriteStatus(Status{LastAttempt: now, LastResult: "failure", LastError: "mysite: boom", LastSuccess: ok})
	if err := s.Health(ctx, now); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("after a failure: %v", err)
	}
	old := now.Add(-3 * 24 * time.Hour)
	s.WriteStatus(Status{LastAttempt: old, LastResult: "success", LastSuccess: old})
	if err := s.Health(ctx, now); err == nil || !strings.Contains(err.Error(), "have not happened") {
		t.Errorf("after missed runs: %v", err)
	}
}

func TestForgetPicksStacks(t *testing.T) {
	s := newTest(t, twoStacks)
	ctx := context.Background()
	if err := s.Forget(ctx, []string{"nope"}); err == nil || !strings.Contains(err.Error(), "no stack named") {
		t.Errorf("unknown stack: %v", err)
	}
	if err := s.Forget(ctx, []string{"shop"}); err == nil || !strings.Contains(err.Error(), "no backup_password") {
		t.Errorf("stack without backup_password: %v", err)
	}
	// Nothing backed up yet: every stack is skipped, which is a success.
	if err := s.Forget(ctx, nil); err != nil {
		t.Errorf("no repositories: %v", err)
	}
	st, ok := s.ReadStatus()
	if !ok || st.LastResult != "success" {
		t.Errorf("status = %+v, %v", st, ok)
	}
	if err := s.Restic(ctx, "shop", []string{"snapshots"}); err == nil {
		t.Error("restic on a stack without backup_password should fail")
	}
}

// TestForgetWithRestic runs real retention when restic is installed.
func TestForgetWithRestic(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic not installed")
	}
	s := newTest(t, twoStacks)
	ctx := context.Background()
	st, _ := s.Cfg.Stack("mysite")
	repo := s.Repo(st)
	if err := repo.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "src")
	os.MkdirAll(src, 0o700)
	for i := 0; i < 3; i++ {
		os.WriteFile(filepath.Join(src, "f"), []byte{byte(i)}, 0o600)
		// Each snapshot from a different host, as different tinnie names would be: keep last 1 applies per host.
		if _, err := (func() (any, error) {
			r := repo
			r.Host = "h"
			return r.Backup(ctx, []string{src}, nil)
		})(); err != nil {
			t.Fatal(err)
		}
	}
	if !s.HasRepository(st) {
		t.Fatal("repository not detected")
	}
	if err := s.Forget(ctx, []string{"mysite"}); err != nil {
		t.Fatal(err)
	}
	latest, err := repo.Latest(ctx)
	if err != nil || latest == nil {
		t.Fatalf("latest = %v, %v", latest, err)
	}
	var out strings.Builder
	s.PrintSummary(ctx, &out)
	if !strings.Contains(out.String(), "last snapshot "+latest.ShortID) || !strings.Contains(out.String(), "rest:https://mysite:<password>@<host>/mysite") {
		t.Errorf("summary:\n%s", out.String())
	}

	// A wrong backup_password shows up in the checks.
	s.Cfg.Stacks[0].BackupPassword = "wrong"
	if p := s.CheckRepositories(ctx); len(p) != 1 || !strings.Contains(p[0], "mysite") {
		t.Errorf("problems = %q", p)
	}
}

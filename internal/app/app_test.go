package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tinnie/internal/config"
)

func testApp(t *testing.T) *App {
	t.Helper()
	cfg, err := config.Parse([]byte(`
schedule: "0 */6 * * *"
repository: {url: /r, password: p}
sources: [{name: db, type: postgres, host: h, database: d, user: u}]
`))
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(cfg, Paths{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestHealth(t *testing.T) {
	a := testApp(t)
	now := time.Date(2026, 11, 1, 13, 0, 0, 0, time.UTC)
	if err := a.Health(now); err == nil {
		t.Error("expected unhealthy with no status")
	}

	write := func(st Status) {
		if err := a.Paths.WriteStatus(st); err != nil {
			t.Fatal(err)
		}
	}
	write(Status{LastResult: "success", LastAttempt: now.Add(-6 * time.Hour), LastSuccess: time.Date(2026, 11, 1, 6, 0, 5, 0, time.UTC)})
	if err := a.Health(now); err != nil {
		t.Errorf("one run behind should be healthy: %v", err)
	}
	write(Status{LastResult: "success", LastSuccess: time.Date(2026, 11, 1, 0, 0, 5, 0, time.UTC)})
	if err := a.Health(now); err == nil {
		t.Error("two runs behind should be unhealthy")
	}
	write(Status{LastResult: "failure", LastError: "boom", LastAttempt: now, LastSuccess: now})
	if err := a.Health(now); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("failed last attempt should be unhealthy, got %v", err)
	}
}

func TestLockIsExclusive(t *testing.T) {
	a := testApp(t)
	unlock, err := a.Paths.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Paths.Lock(); !errors.Is(err, ErrBusy) {
		t.Errorf("second lock = %v, want ErrBusy", err)
	}
	unlock()
	again, err := a.Paths.Lock()
	if err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	again()
}

func TestPickSources(t *testing.T) {
	a := testApp(t)
	if got, _ := a.pick(nil); len(got) != 1 {
		t.Errorf("pick(nil) = %d sources", len(got))
	}
	if _, err := a.pick([]string{"nope"}); err == nil || !strings.Contains(err.Error(), "sources: db") {
		t.Errorf("unknown source error = %v", err)
	}
}

func TestVolumesAreNotStaged(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.Parse([]byte(`
schedule: "@daily"
repository: {url: /r, password: p}
sources:
  - {name: db, type: mysql, host: h, database: d, user: u}
  - {name: files, type: volume, path: /sources/files}
`))
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(cfg, Paths{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"db", "files", "old"} {
		if err := os.MkdirAll(filepath.Join(a.Paths.Stage(), d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.pruneStage(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(a.Paths.Stage())
	if len(entries) != 1 || entries[0].Name() != "db" {
		t.Errorf("stage holds %v, want only db", entries)
	}

}

func TestVolumesStayClearOfStateAndRepository(t *testing.T) {
	tests := []struct{ path, repo, want string }{
		{"/var/lib/tinnie/stage", "/backups/restic", "volume path /var/lib/tinnie/stage is inside tinnie's state dir /var/lib/tinnie"},
		{"/var/lib", "/backups/restic", "volume path /var/lib contains tinnie's state dir /var/lib/tinnie"},
		{"/var/lib/tinnie", "/backups/restic", "volume path /var/lib/tinnie is inside tinnie's state dir"},
		{"/backups", "/backups/restic", "volume path /backups contains the repository /backups/restic"},
		{"/backups", "local:/backups/restic", "volume path /backups contains the repository /backups/restic"},
		{"/backups/restic/data", "/backups/restic/", "volume path /backups/restic/data is inside the repository /backups/restic"},
		{"/backups", "s3:s3.amazonaws.com/backups", ""},
		{"/backups", "sftp:me@host:/backups", ""},
		{"/sources/var", "/backups/restic", ""},
		{"/var/lib/tinnie2", "/backups/restic2", ""},
	}
	for _, tt := range tests {
		cfg, err := config.Parse([]byte("schedule: \"@daily\"\nrepository: {url: \"" + tt.repo + "\", password: p}\nsources: [{name: files, type: volume, path: " + tt.path + "}]\n"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = New(cfg, Paths{Root: "/var/lib/tinnie"})
		if (tt.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tt.want)) {
			t.Errorf("path %s, repository %s: %v, want %q", tt.path, tt.repo, err, tt.want)
		}
	}
}

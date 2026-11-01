package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tinnie/internal/config"
	"tinnie/internal/docker"
)

// fakeDocker keeps container states in memory. tinnie itself is the container "self" of service "backup".
type fakeDocker struct {
	containers map[string][]docker.Container
	failStop   string
	failStart  string
	// stopping counts inspections that still see a container running after its stop returned.
	stopping map[string]int
	onStop   func()
	calls    []string
	inspects int
}

func (f *fakeDocker) Host() string                                { return "unix:///fake.sock" }
func (f *fakeDocker) Ping(ctx context.Context) error              { return nil }
func (f *fakeDocker) Project(ctx context.Context) (string, error) { return "shop", nil }

func (f *fakeDocker) Self(ctx context.Context) (docker.Container, error) {
	return docker.Container{ID: "self", State: "running"}, nil
}

func (f *fakeDocker) ServiceContainers(ctx context.Context, service string) ([]docker.Container, error) {
	if cs, ok := f.containers[service]; ok {
		return append([]docker.Container{}, cs...), nil
	}
	return nil, fmt.Errorf("no container for service %q in Compose project \"shop\"", service)
}

func (f *fakeDocker) find(id string) *docker.Container {
	for _, cs := range f.containers {
		for i := range cs {
			if cs[i].ID == id {
				return &cs[i]
			}
		}
	}
	return nil
}

func (f *fakeDocker) Inspect(ctx context.Context, id string) (docker.Container, error) {
	f.inspects++
	c := f.find(id)
	if c == nil {
		return docker.Container{}, fmt.Errorf("No such container: %s: %w", id, docker.ErrNotFound)
	}
	if f.stopping[id] > 0 {
		f.stopping[id]--
		return docker.Container{ID: id, State: "running"}, nil
	}
	return *c, nil
}

func (f *fakeDocker) Stop(ctx context.Context, id string, timeout time.Duration) error {
	f.calls = append(f.calls, "stop "+id)
	if f.onStop != nil {
		f.onStop()
	}
	if id == f.failStop {
		return errors.New("boom")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f.find(id).State = "exited"
	return nil
}

func (f *fakeDocker) Start(ctx context.Context, id string) error {
	f.calls = append(f.calls, "start "+id)
	c := f.find(id)
	switch {
	case c == nil:
		return fmt.Errorf("No such container: %s: %w", id, docker.ErrNotFound)
	case id == f.failStart:
		return errors.New("port is already allocated")
	case f.stopping[id] > 0:
		return errors.New("started while still stopping")
	}
	c.State = "running"
	return ctx.Err()
}

func newFake() *fakeDocker {
	stopWait = 300 * time.Millisecond
	return &fakeDocker{stopping: map[string]int{}, containers: map[string][]docker.Container{
		"php-fpm": {{ID: "php1", State: "running"}, {ID: "php2", State: "running"}},
		"nginx":   {{ID: "web", Name: "shop-nginx-1", State: "running"}},
		"cron":    {{ID: "cron", State: "exited"}},
		"backup":  {{ID: "self", State: "running"}},
	}}
}

func TestStopServices(t *testing.T) {
	a := testApp(t)
	f := newFake()
	a.Docker = f
	restart, err := a.stopServices(context.Background(), []string{"php-fpm", "cron", "nginx"})
	if err != nil {
		t.Fatal(err)
	}
	if err := restart(); err != nil {
		t.Fatal(err)
	}
	if err := restart(); err != nil {
		t.Fatal(err)
	}
	want := "stop php1,stop php2,stop web,start web,start php2,start php1"
	if got := strings.Join(f.calls, ","); got != want {
		t.Errorf("calls = %s, want %s", got, want)
	}
}

func TestStopServicesRestartsAfterFailure(t *testing.T) {
	a := testApp(t)
	f := newFake()
	f.failStop = "web"
	a.Docker = f
	restart, err := a.stopServices(context.Background(), []string{"php-fpm", "nginx", "mailer"})
	if err == nil || !strings.Contains(err.Error(), "could not stop nginx") {
		t.Fatalf("err = %v", err)
	}
	restart()
	want := "stop php1,stop php2,stop web,start web,start php2,start php1"
	if got := strings.Join(f.calls, ","); got != want {
		t.Errorf("calls = %s, want %s", got, want)
	}

	f.calls = nil
	f.failStop = ""
	restart, err = a.stopServices(context.Background(), []string{"php-fpm", "mailer"})
	if err == nil || !strings.Contains(err.Error(), `service "mailer"`) {
		t.Fatalf("unknown service err = %v", err)
	}
	restart()
	if got := strings.Join(f.calls, ","); got != "stop php1,stop php2,start php2,start php1" {
		t.Errorf("calls = %s", got)
	}
}

func TestStopServicesRestartsAfterCancel(t *testing.T) {
	a := testApp(t)
	f := newFake()
	a.Docker = f
	ctx, cancel := context.WithCancel(context.Background())
	restart, err := a.stopServices(ctx, []string{"nginx"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := restart(); err != nil {
		t.Errorf("restart after cancel: %v", err)
	}
	if got := strings.Join(f.calls, ","); got != "stop web,start web" {
		t.Errorf("calls = %s", got)
	}
}

func TestNoDockerWithoutStopOrCommand(t *testing.T) {
	if a := testApp(t); a.Docker != nil {
		t.Error("Docker client created for a config without stop or command sources")
	}
	restart, err := (&App{}).stopServices(context.Background(), nil)
	if err != nil || restart() != nil {
		t.Error("empty stop list should do nothing")
	}
}

func TestRestoreTargetsSkipsCommandsWithoutRestore(t *testing.T) {
	cfg, err := config.Parse([]byte(`
schedule: "@daily"
repository: {url: /r, password: p}
stop: [php-fpm]
sources:
  - {name: db, type: mysql, host: h, database: d, user: u}
  - {name: gitea, type: command, service: gitea, backup: gitea dump}
`))
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(cfg, Paths{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if a.Docker == nil {
		t.Error("no Docker client for a config with stop")
	}
	targets, skipped, err := a.restoreTargets(nil)
	if err != nil || len(targets) != 1 || targets[0].Name() != "db" || len(skipped) != 1 {
		t.Errorf("restore all = %v, %v, %v", targets, skipped, err)
	}
	if _, _, err := a.restoreTargets([]string{"gitea"}); err == nil || !strings.Contains(err.Error(), "gitea: no restore command configured") {
		t.Errorf("restore gitea = %v", err)
	}
	if _, _, err := a.restoreTargets([]string{"db"}); err != nil {
		t.Errorf("restore db = %v", err)
	}
}

// fakeRestic puts a restic on PATH that logs its arguments to <repository>/calls, saves snapshot
// 0123456789abcdef, and only knows the snapshot "latest", in which every path asked for is a folder.
func fakeRestic(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
echo "$*" >> "$RESTIC_REPOSITORY/calls"
case "$1" in
backup) echo '{"message_type":"summary","snapshot_id":"0123456789abcdef"}' ;;
ls)
	snap=$5; shift 6
	[ "$snap" = latest ] || { echo "Fatal: no matching ID found for prefix \"$snap\"" >&2; exit 1; }
	echo '{"message_type":"snapshot","id":"0123456789abcdef"}'
	for p in "$@"; do printf '{"message_type":"node","path":"%s","type":"dir","uid":%s,"gid":%s,"mode":493}\n' "$p" "$(id -u)" "$(id -g)"; done ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "restic"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
}

// stopApp is an app with one volume source, a fake restic repository and nginx under stop.
func stopApp(t *testing.T) (*App, *fakeDocker) {
	t.Helper()
	fakeRestic(t)
	dir := t.TempDir()
	vol := filepath.Join(dir, "vol")
	os.Mkdir(vol, 0o755)
	os.Mkdir(filepath.Join(dir, "repo"), 0o700)
	cfg, err := config.Parse([]byte(fmt.Sprintf(`
schedule: "@daily"
repository: {url: %s/repo, password: p}
stop: [nginx]
sources: [{name: files, type: volume, path: %s}]
`, dir, vol)))
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(cfg, Paths{Root: filepath.Join(dir, "state")})
	if err != nil {
		t.Fatal(err)
	}
	f := newFake()
	a.Docker = f
	return a, f
}

func TestFailedRestartFailsTheBackupWithoutRetry(t *testing.T) {
	a, f := stopApp(t)
	f.failStart = "web"
	err := a.Backup(context.Background(), 3)
	if err == nil || !strings.Contains(err.Error(), "could not start nginx (container shop-nginx-1) again, so start it by hand: port is already allocated") {
		t.Fatalf("backup = %v", err)
	}
	if got := strings.Join(f.calls, ","); got != "stop web,start web" {
		t.Errorf("calls = %s (a retry would stop nothing and succeed)", got)
	}
	if st, _ := a.Paths.ReadStatus(); st.LastResult != "failure" {
		t.Errorf("status = %+v", st)
	}
	if list, _ := a.Paths.ReadStopped(); len(list) != 1 || list[0].ID != "web" {
		t.Errorf("stopped list = %+v", list)
	}

	// The next backup starts it first, and fails while it cannot.
	f.calls = nil
	if err := a.Backup(context.Background(), 3); err == nil || !strings.Contains(err.Error(), "services stopped by an earlier backup or restore are still down") {
		t.Errorf("backup with nginx still down = %v", err)
	}
	if got := strings.Join(f.calls, ","); got != "start web" {
		t.Errorf("calls = %s", got)
	}

	f.calls, f.failStart = nil, ""
	if err := a.Backup(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.calls, ","); got != "start web,stop web,start web" {
		t.Errorf("calls = %s", got)
	}
	if _, err := os.Stat(a.Paths.Stopped()); !os.IsNotExist(err) {
		t.Errorf("stopped list left behind: %v", err)
	}
	if st, _ := a.Paths.ReadStatus(); st.LastResult != "success" || st.LastSnapshot != "01234567" {
		t.Errorf("status = %+v", st)
	}
}

func TestLeftoversAreStartedOnRunStartAndBeforeRestore(t *testing.T) {
	a, f := stopApp(t)
	f.find("web").State = "exited"
	if err := a.Paths.WriteStopped([]stoppedContainer{{"nginx", "web", "shop-nginx-1"}, {"gone", "old", "shop-gone-1"}}); err != nil {
		t.Fatal(err)
	}
	f.failStart = "web"
	a.startLeftoversOnStart(context.Background())
	if st, _ := a.Paths.ReadStatus(); st.LastResult != "failure" || !strings.Contains(st.LastError, "could not start nginx") {
		t.Errorf("status after a failed start = %+v", st)
	}
	if list, _ := a.Paths.ReadStopped(); len(list) != 1 || list[0].ID != "web" {
		t.Errorf("stopped list = %+v (a container that no longer exists is dropped)", list)
	}
	err := a.Restore(context.Background(), RestoreOptions{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "still down") {
		t.Errorf("restore with nginx down = %v", err)
	}

	f.calls, f.failStart = nil, ""
	if err := a.Restore(context.Background(), RestoreOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.calls, ","); got != "start web,stop web,start web" {
		t.Errorf("calls = %s", got)
	}
	if list, _ := a.Paths.ReadStopped(); len(list) != 0 {
		t.Errorf("stopped list = %+v", list)
	}
}

func TestRestoreChecksSnapshotBeforeStopping(t *testing.T) {
	a, f := stopApp(t)
	err := a.Restore(context.Background(), RestoreOptions{Snapshot: "deadbeef", Yes: true})
	if err == nil || !strings.Contains(err.Error(), "no matching ID") {
		t.Errorf("restore of a missing snapshot = %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("services were bounced: %v", f.calls)
	}
}

func TestStopIsNotCancelledAndStartWaitsForIt(t *testing.T) {
	a := testApp(t)
	f := newFake()
	a.Docker = f
	ctx, cancel := context.WithCancel(context.Background())
	f.onStop = cancel // tinnie is interrupted while the first container stops
	f.stopping["php1"] = 2
	restart, err := a.stopServices(ctx, []string{"php-fpm"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if err := restart(); err != nil {
		t.Fatalf("restart = %v", err)
	}
	if got := strings.Join(f.calls, ","); got != "stop php1,start php1" {
		t.Errorf("calls = %s", got)
	}
	if f.inspects != 3 {
		t.Errorf("inspected %d times, want 3 (twice still stopping, then stopped)", f.inspects)
	}

	// A container that never stops is started after stopWait anyway, which is then an error.
	f.calls = nil
	f.stopping["web"] = 1000
	restart, _ = a.stopServices(context.Background(), []string{"nginx"})
	if err := restart(); err == nil || !isRestartError(err) {
		t.Errorf("restart of a container still stopping = %v", err)
	}
}

func TestNeverStopsItself(t *testing.T) {
	a, f := stopApp(t)
	a.Cfg.Stop.Backup = []string{"backup", "nginx"}
	restart, err := a.stopServices(context.Background(), a.Cfg.Stop.Backup)
	if err != nil {
		t.Fatal(err)
	}
	restart()
	if got := strings.Join(f.calls, ","); got != "stop web,start web" {
		t.Errorf("calls = %s", got)
	}

	out := captureStdout(t, func() {
		if err := a.Check(context.Background()); err == nil || err.Error() != "1 check(s) failed" {
			t.Errorf("check = %v", err)
		}
	})
	if !strings.Contains(out, "✗ stop backup: that is tinnie's own container") || !strings.Contains(out, "✓ stop nginx") {
		t.Errorf("check printed:\n%s", out)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	fn()
	os.Stdout = stdout
	w.Close()
	return <-done
}

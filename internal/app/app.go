package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"tinnie/internal/config"
	"tinnie/internal/docker"
	"tinnie/internal/proc"
	"tinnie/internal/restic"
	"tinnie/internal/source"
)

type App struct {
	Cfg     *config.Config
	Paths   Paths
	Sources []source.Source
	Repo    restic.Repo
	// Docker is set only when the config stops services or has command sources; otherwise Docker is never touched.
	Docker Docker
}

// Docker is the part of the Docker client the app uses for stop lists.
type Docker interface {
	Host() string
	Ping(ctx context.Context) error
	Project(ctx context.Context) (string, error)
	Self(ctx context.Context) (docker.Container, error)
	ServiceContainers(ctx context.Context, service string) ([]docker.Container, error)
	Inspect(ctx context.Context, id string) (docker.Container, error)
	Stop(ctx context.Context, id string, timeout time.Duration) error
	Start(ctx context.Context, id string) error
}

const (
	stopTimeout  = 30 * time.Second
	startTimeout = time.Minute
)

// stopWait is how long a start waits for a container to finish stopping: a stop that tinnie gave up on keeps
// going in Docker, and would stop the container again right after it was started.
var stopWait = stopTimeout + 5*time.Second

// restartError is a failure to start a service tinnie stopped. A backup is not retried after one: the retry
// would find the service stopped, leave it so, and report success while the app stays down.
type restartError struct{ err error }

func (e *restartError) Error() string { return e.err.Error() }
func (e *restartError) Unwrap() error { return e.err }

func isRestartError(err error) bool {
	var re *restartError
	return errors.As(err, &re)
}

func New(cfg *config.Config, paths Paths) (*App, error) {
	// One client for stop lists and command sources, created only when the config needs Docker.
	var client *docker.Client
	var sd source.Docker
	if cfg.NeedsDocker() {
		var err error
		if client, err = docker.FromEnv(); err != nil {
			return nil, err
		}
		sd = client
	}
	sources, err := source.All(cfg, sd)
	if err != nil {
		return nil, err
	}
	if err := checkOwnPaths(sources, paths.Root, cfg.Repository.URL); err != nil {
		return nil, err
	}
	a := &App{
		Cfg:     cfg,
		Paths:   paths,
		Sources: sources,
		Repo: restic.Repo{
			URL:      cfg.Repository.URL,
			Password: cfg.Repository.Password,
			Env:      cfg.Repository.Env,
			Host:     cfg.Name,
			CacheDir: paths.Cache(),
		},
	}
	if client != nil {
		a.Docker = client
	}
	return a, nil
}

// checkOwnPaths rejects a volume path that overlaps tinnie's state folder or a local repository, in either
// direction: backing the volume up would back them up too, and restoring it would delete them.
func checkOwnPaths(sources []source.Source, root, repoURL string) error {
	type own struct{ what, path string }
	owns := []own{{"tinnie's state dir", filepath.Clean(root)}}
	if repo := strings.TrimPrefix(repoURL, "local:"); filepath.IsAbs(repo) {
		owns = append(owns, own{"the repository", filepath.Clean(repo)})
	}
	for _, s := range sources {
		d, ok := s.(source.Direct)
		if !ok {
			continue
		}
		for _, o := range owns {
			how := ""
			switch {
			case within(d.Path(), o.path):
				how = "is inside"
			case within(o.path, d.Path()):
				how = "contains"
			default:
				continue
			}
			return fmt.Errorf("source %s: volume path %s %s %s %s; mount the volume somewhere else, such as /sources/%s", s.Name(), d.Path(), how, o.what, o.path, s.Name())
		}
	}
	return nil
}

// within reports whether p is dir or inside it.
func within(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}

// Backup tries up to attempts times, a minute apart, and only reports the final outcome.
func (a *App) Backup(ctx context.Context, attempts int) error {
	unlock, err := a.Paths.Lock()
	if err != nil {
		return err
	}
	defer unlock()

	started := time.Now()
	var snapshot string
	if err = a.startLeftovers(); err == nil {
		for i := 1; ; i++ {
			snapshot, err = a.backup(ctx)
			if err == nil || i >= attempts || ctx.Err() != nil || isRestartError(err) {
				break
			}
			proc.Logf("backup failed (attempt %d of %d): %v; retrying in a minute", i, attempts, err)
			select {
			case <-ctx.Done():
			case <-time.After(time.Minute):
			}
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	a.record(ctx, started, snapshot, err)
	return err
}

// record saves the outcome of a run as the status the healthcheck reads, and notifies.
func (a *App) record(ctx context.Context, started time.Time, snapshot string, err error) {
	duration := time.Since(started)
	st, _ := a.Paths.ReadStatus()
	st.LastAttempt = started
	if err != nil {
		st.LastResult, st.LastError = "failure", err.Error()
	} else {
		st.LastResult, st.LastError, st.LastSuccess, st.LastSnapshot = "success", "", started, snapshot
	}
	if werr := a.Paths.WriteStatus(st); werr != nil {
		proc.Logf("could not save status: %v", werr)
	}
	notify(ctx, a.Cfg.Notify, result{Name: a.Cfg.Name, Snapshot: snapshot, Started: started, Duration: duration, Err: err})
}

func (a *App) backup(ctx context.Context) (_ string, err error) {
	if err := a.Repo.Ensure(ctx); err != nil {
		return "", err
	}
	restart, err := a.stopServices(ctx, a.Cfg.Stop.Backup)
	defer func() { err = errors.Join(err, restart()) }()
	if err != nil {
		return "", err
	}
	stage := a.Paths.Stage()
	if err := a.pruneStage(); err != nil {
		return "", err
	}
	paths := []string{stage}
	var excludes []string
	for _, s := range a.Sources {
		proc.Logf("backing up %s (%s %s)", s.Name(), s.Type(), s.Describe())
		if d, ok := s.(source.Direct); ok {
			if err := s.Backup(ctx, ""); err != nil {
				return "", fmt.Errorf("%s: %w", s.Name(), err)
			}
			paths = append(paths, d.Path())
			for _, ex := range d.Excludes() {
				excludes = append(excludes, d.Path()+"/"+ex)
			}
			continue
		}
		dir := filepath.Join(stage, s.Name())
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		if err := s.Backup(ctx, dir); err != nil {
			return "", fmt.Errorf("%s: %w", s.Name(), err)
		}
	}
	proc.Logf("saving snapshot")
	sum, err := a.Repo.Backup(ctx, paths, excludes)
	if err = errors.Join(err, restart()); err != nil {
		return "", err
	}
	short := shortID(sum.SnapshotID)
	proc.Logf("snapshot %s saved: %d new and %d changed files, %s added", short, sum.FilesNew, sum.FilesChanged, humanBytes(sum.DataAdded))
	if !a.Cfg.Repository.Keep.Empty() {
		proc.Logf("removing snapshots outside the keep rules")
		if err := a.Repo.Forget(ctx, a.Cfg.Repository.Keep); err != nil {
			return short, err
		}
	}
	proc.Logf("backup finished")
	return short, nil
}

// stopServices stops the running containers of each service, in order, and returns a function that starts
// exactly those again, in reverse order. Call that function whatever happens, including when stopServices
// fails part way: it is safe to call more than once and does not use ctx, so a cancelled backup or restore
// still brings the services back. Each container is added to the stopped list before it is stopped, so if
// tinnie dies before starting it again, the next run does. tinnie's own container is never stopped.
func (a *App) stopServices(ctx context.Context, services []string) (restart func() error, err error) {
	var down []stoppedContainer
	done := false
	restart = func() error {
		if done || len(down) == 0 {
			return nil
		}
		done = true
		return a.startContainers(down)
	}
	if len(services) == 0 {
		return restart, nil
	}
	self, err := a.Docker.Self(ctx)
	if err != nil {
		return restart, err
	}
	for _, service := range services {
		containers, err := a.Docker.ServiceContainers(ctx, service)
		if err != nil {
			return restart, err
		}
		logged := false
		for _, c := range containers {
			if c.ID == self.ID {
				proc.Logf("not stopping %s: it is tinnie itself", service)
				logged = true
				continue
			}
			if !c.Running() {
				continue
			}
			if err := ctx.Err(); err != nil {
				return restart, err
			}
			if !logged {
				proc.Logf("stopping %s", service)
				logged = true
			}
			// Remember it before stopping, so a stop that fails half way is still started again.
			list := append(down, stoppedContainer{service, c.ID, c.Name})
			if err := a.Paths.WriteStopped(list); err != nil {
				return restart, fmt.Errorf("could not save the list of stopped services, so %s was not stopped: %w", service, err)
			}
			down = list
			// Not ctx: Docker keeps stopping a container after the request is cancelled, so the stop is
			// always seen through and the start that follows is not undone by it.
			sctx, cancel := context.WithTimeout(context.Background(), stopTimeout+10*time.Second)
			err := a.Docker.Stop(sctx, c.ID, stopTimeout)
			cancel()
			if err != nil {
				return restart, fmt.Errorf("could not stop %s: %w", service, err)
			}
		}
		if !logged {
			proc.Logf("%s is not running, so it is left as it is", service)
		}
	}
	return restart, nil
}

// startContainers starts stopped containers in reverse order and keeps the ones that could not be started
// in the stopped list, so the next run tries again. Containers that no longer exist are dropped.
func (a *App) startContainers(list []stoppedContainer) error {
	var errs []error
	var left []stoppedContainer
	for i := len(list) - 1; i >= 0; i-- {
		s := list[i]
		if i == len(list)-1 || list[i+1].Service != s.Service {
			proc.Logf("starting %s", s.Service)
		}
		if err := a.startAgain(s); err != nil {
			errs = append(errs, fmt.Errorf("could not start %s (container %s) again, so start it by hand: %w", s.Service, s.Name, err))
			left = append([]stoppedContainer{s}, left...)
		}
	}
	if err := a.Paths.WriteStopped(left); err != nil {
		errs = append(errs, fmt.Errorf("could not update the list of stopped services: %w", err))
	}
	if len(errs) > 0 {
		return &restartError{errors.Join(errs...)}
	}
	return nil
}

// startAgain waits for a container to finish stopping, then starts it.
func (a *App) startAgain(s stoppedContainer) error {
	ctx, cancel := context.WithTimeout(context.Background(), stopWait+startTimeout)
	defer cancel()
	gone := func(err error) bool {
		if errors.Is(err, docker.ErrNotFound) {
			proc.Logf("%s (container %s) no longer exists, so it is not started again", s.Service, s.Name)
			return true
		}
		return false
	}
	deadline := time.Now().Add(stopWait)
	for {
		c, err := a.Docker.Inspect(ctx, s.ID)
		if gone(err) {
			return nil
		}
		if err != nil || !c.Running() || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err := a.Docker.Start(ctx, s.ID); err != nil && !gone(err) {
		return err
	}
	return nil
}

// startLeftovers starts the containers an earlier backup or restore stopped and never started again,
// because starting them failed or tinnie was killed first. It runs when tinnie starts and before every
// backup and restore, and fails while any of them cannot be started.
func (a *App) startLeftovers() error {
	list, err := a.Paths.ReadStopped()
	if err != nil || len(list) == 0 {
		return err
	}
	if a.Docker == nil {
		// stop has gone from the config since, but the services are still down.
		d, err := docker.FromEnv()
		if err != nil {
			return err
		}
		a.Docker = d
	}
	var names []string
	for _, s := range list {
		names = append(names, s.Service)
	}
	proc.Logf("an earlier backup or restore left %s stopped; starting it again", strings.Join(slices.Compact(names), ", "))
	if err := a.startContainers(list); err != nil {
		return fmt.Errorf("services stopped by an earlier backup or restore are still down: %w", err)
	}
	return nil
}

// pruneStage removes folders of sources that are no longer staged (removed, or now volumes) so snapshots
// only hold current sources. It only ever looks inside the stage folder, never at volume paths.
func (a *App) pruneStage() error {
	stage := a.Paths.Stage()
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, s := range a.Sources {
		if _, direct := s.(source.Direct); !direct {
			keep[s.Name()] = true
		}
	}
	for _, e := range entries {
		if !keep[e.Name()] {
			if err := os.RemoveAll(filepath.Join(stage, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *App) Check(ctx context.Context) error {
	var failed []string
	report := func(what string, err error) {
		if err != nil {
			failed = append(failed, what)
			fmt.Printf("  ✗ %s: %v\n", what, err)
		} else {
			fmt.Printf("  ✓ %s\n", what)
		}
	}
	fmt.Println("Config is valid. Checking connections:")
	if a.Docker != nil {
		err := a.Docker.Ping(ctx)
		if err == nil {
			_, err = a.Docker.Project(ctx)
		}
		what := "Docker at " + a.Docker.Host()
		if err != nil {
			what = "Docker"
		}
		report(what, err)
		if err == nil {
			self, _ := a.Docker.Self(ctx)
			seen := map[string]bool{}
			for _, service := range append(append([]string{}, a.Cfg.Stop.Backup...), a.Cfg.Stop.Restore...) {
				if !seen[service] {
					seen[service] = true
					cs, err := a.Docker.ServiceContainers(ctx, service)
					for _, c := range cs {
						if c.ID == self.ID {
							err = errors.New("that is tinnie's own container")
						}
					}
					report("stop "+service, err)
				}
			}
		}
	}
	for _, s := range a.Sources {
		report(fmt.Sprintf("%s (%s %s)", s.Name(), s.Type(), s.Describe()), s.Check(ctx))
	}
	report("repository "+restic.Redact(a.Cfg.Repository.URL), a.Repo.Ensure(ctx))
	if len(failed) > 0 {
		return fmt.Errorf("%d check(s) failed", len(failed))
	}
	return nil
}

type RestoreOptions struct {
	Snapshot string
	Sources  []string
	Yes      bool
}

func (a *App) Restore(ctx context.Context, opts RestoreOptions) (err error) {
	if opts.Snapshot == "" {
		opts.Snapshot = "latest"
	}
	targets, skipped, err := a.restoreTargets(opts.Sources)
	if err != nil {
		return err
	}
	fmt.Printf("This replaces the following with snapshot %s:\n", opts.Snapshot)
	for _, s := range targets {
		fmt.Printf("  %-12s %s %s\n", s.Name(), s.Type(), s.Describe())
	}
	for _, s := range skipped {
		fmt.Printf("Skipping %s: it has no restore command.\n", s.Name())
	}
	if stop := a.Cfg.Stop.Restore; len(stop) > 0 {
		them := "it"
		if len(stop) > 1 {
			them = "them"
		}
		fmt.Printf("Everything added since that snapshot is deleted. tinnie stops %s while it restores and starts %s again afterwards.\n", strings.Join(stop, ", "), them)
	} else {
		fmt.Println("Everything added since that snapshot is deleted. Stop the apps that write to these first.")
	}
	if !opts.Yes {
		if err := confirm("Type restore to continue: ", "restore"); err != nil {
			return err
		}
	}

	unlock, err := a.Paths.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err := a.startLeftovers(); err != nil {
		return err
	}

	// Check everything before stopping or changing anything, so a missing mount, a wrong snapshot id or a
	// partial snapshot never bounces services or half-restores.
	var staged []source.Source
	var paths []string
	for _, s := range targets {
		if d, ok := s.(source.Direct); ok {
			if err := s.Check(ctx); err != nil {
				return fmt.Errorf("%s: %w", s.Name(), err)
			}
			paths = append(paths, d.Path())
		} else {
			staged = append(staged, s)
			paths = append(paths, a.stageDir(s))
		}
	}
	snapshot, nodes, err := a.Repo.Lookup(ctx, opts.Snapshot, paths)
	if err != nil {
		return err
	}
	for i, s := range targets {
		if n, ok := nodes[paths[i]]; !ok || n.Type != "dir" {
			return fmt.Errorf("snapshot %s has no data for %s", opts.Snapshot, s.Name())
		}
	}

	restart, err := a.stopServices(ctx, a.Cfg.Stop.Restore)
	defer func() { err = errors.Join(err, restart()) }()
	if err != nil {
		return err
	}
	proc.Logf("restoring snapshot %s", shortID(snapshot))
	for _, s := range skipped {
		proc.Logf("skipping %s: %v", s.Name(), source.ErrNoRestore)
	}

	if len(staged) > 0 {
		target := a.Paths.Restore()
		if err := os.RemoveAll(target); err != nil {
			return err
		}
		defer os.RemoveAll(target)
		var includes []string
		for _, s := range staged {
			includes = append(includes, a.stageDir(s))
		}
		if err := a.Repo.Restore(ctx, snapshot, target, includes); err != nil {
			return err
		}
	}
	for i, s := range targets {
		proc.Logf("replacing %s (%s %s)", s.Name(), s.Type(), s.Describe())
		var err error
		if d, ok := s.(source.Direct); ok {
			err = a.Repo.RestoreInPlace(ctx, snapshot, nodes[paths[i]], d.Excludes())
		} else {
			err = s.Restore(ctx, filepath.Join(a.Paths.Restore(), a.stageDir(s)))
		}
		if err != nil {
			return fmt.Errorf("%s: %w", s.Name(), err)
		}
	}
	proc.Logf("restore finished")
	return nil
}

// restoreTargets picks the sources to restore. Restoring everything skips sources that cannot be restored
// (a command source without a restore command); naming such a source is an error.
func (a *App) restoreTargets(names []string) (targets, skipped []source.Source, err error) {
	picked, err := a.pick(names)
	if err != nil {
		return nil, nil, err
	}
	for _, s := range picked {
		switch {
		case source.Restorable(s):
			targets = append(targets, s)
		case len(names) > 0:
			return nil, nil, fmt.Errorf("%s: %w; add restore: to the source to restore it", s.Name(), source.ErrNoRestore)
		default:
			skipped = append(skipped, s)
		}
	}
	if len(targets) == 0 {
		return nil, nil, errors.New("none of the sources can be restored: add restore: to the command sources")
	}
	return targets, skipped, nil
}

// stageDir is where a staged source's files sit, both on disk and inside snapshots.
func (a *App) stageDir(s source.Source) string {
	return filepath.Join(a.Paths.Stage(), s.Name())
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func (a *App) pick(names []string) ([]source.Source, error) {
	if len(names) == 0 {
		return a.Sources, nil
	}
	byName := map[string]source.Source{}
	var known []string
	for _, s := range a.Sources {
		byName[s.Name()] = s
		known = append(known, s.Name())
	}
	var out []source.Source
	for _, n := range names {
		s, ok := byName[n]
		if !ok {
			return nil, fmt.Errorf("no source named %q (sources: %s)", n, strings.Join(known, ", "))
		}
		out = append(out, s)
	}
	return out, nil
}

func confirm(prompt, word string) error {
	if fi, err := os.Stdin.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return errors.New("not running in a terminal: add --yes to confirm")
	}
	fmt.Print(prompt)
	var answer string
	fmt.Scanln(&answer)
	if strings.TrimSpace(answer) != word {
		return errors.New("cancelled")
	}
	return nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

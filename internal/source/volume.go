package source

import (
	"context"
	"fmt"
	"os"

	"tinnie/internal/config"
)

// Direct is a source restic backs up and restores in place instead of through the stage folder.
type Direct interface {
	Path() string
	Excludes() []string
}

type volume struct{ cfg config.Volume }

func (v *volume) Name() string       { return v.cfg.Name }
func (v *volume) Type() string       { return "volume" }
func (v *volume) Describe() string   { return v.cfg.Path }
func (v *volume) Path() string       { return v.cfg.Path }
func (v *volume) Excludes() []string { return v.cfg.Exclude }

// Check requires a real folder: restic backs up a symlink as a symlink, not what it points to.
func (v *volume) Check(ctx context.Context) error {
	fi, err := os.Lstat(v.cfg.Path)
	if err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink; mount the volume directly at %s", v.cfg.Path, v.cfg.Path)
	}
	if err != nil || !fi.IsDir() {
		return fmt.Errorf("%s is not a folder: mount the volume at %s in the tinnie service", v.cfg.Path, v.cfg.Path)
	}
	return nil
}

// Backup only checks the volume is mounted: restic reads it in place along with the stage folder.
func (v *volume) Backup(ctx context.Context, dir string) error { return v.Check(ctx) }

// Restore is never called: the app restores a Direct source in place with restic (Repo.RestoreInPlace)
// instead of handing it a folder. It fails rather than doing nothing, should that ever change.
func (v *volume) Restore(ctx context.Context, dir string) error {
	return fmt.Errorf("volume %s is restored in place by restic, not from a folder", v.cfg.Name)
}

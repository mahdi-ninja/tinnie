package source

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"tinnie/internal/config"
	"tinnie/internal/proc"
)

const dumpFile = "database.dump"

const dropSchemas = `DO $$
DECLARE s text;
BEGIN
  FOR s IN SELECT nspname FROM pg_namespace WHERE nspname NOT LIKE 'pg\_%' AND nspname <> 'information_schema' LOOP
    EXECUTE format('DROP SCHEMA %I CASCADE', s);
  END LOOP;
END $$;
CREATE SCHEMA public;`

type postgres struct{ cfg config.Postgres }

func (p *postgres) Name() string { return p.cfg.Name }
func (p *postgres) Type() string { return "postgres" }

func (p *postgres) Describe() string {
	return fmt.Sprintf("%s@%s:%d/%s", p.cfg.User, p.cfg.Host, p.cfg.Port, p.cfg.Database)
}

func (p *postgres) env() map[string]string {
	env := map[string]string{
		"PGHOST":     p.cfg.Host,
		"PGPORT":     strconv.Itoa(p.cfg.Port),
		"PGUSER":     p.cfg.User,
		"PGDATABASE": p.cfg.Database,
	}
	if p.cfg.Password != "" {
		env["PGPASSWORD"] = p.cfg.Password
	}
	if p.cfg.SSLMode != "" {
		env["PGSSLMODE"] = p.cfg.SSLMode
	}
	return env
}

func (p *postgres) Check(ctx context.Context) error {
	return proc.RunQuiet(ctx, p.env(), "psql", "-X", "-v", "ON_ERROR_STOP=1", "-c", "SELECT 1")
}

const clientRoot = "/usr/libexec"

// tool picks the client binary of the server's major version, since newer pg_dump and pg_restore
// emit settings older servers reject. It uses the oldest newer client if there is no exact match.
func (p *postgres) tool(ctx context.Context, name string) (string, error) {
	out, err := proc.Output(ctx, p.env(), "psql", "-XtAc", "SHOW server_version_num")
	if err != nil {
		return "", fmt.Errorf("could not read the server version: %w", err)
	}
	num, err := strconv.Atoi(out)
	if err != nil {
		return "", fmt.Errorf("unexpected server version %q", out)
	}
	major := num / 10000
	var installed []int
	dirs, _ := filepath.Glob(filepath.Join(clientRoot, "postgresql[0-9]*"))
	for _, d := range dirs {
		if v, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(d), "postgresql")); err == nil {
			installed = append(installed, v)
		}
	}
	sort.Ints(installed)
	for _, v := range installed {
		if v >= major {
			return filepath.Join(clientRoot, fmt.Sprintf("postgresql%d", v), name), nil
		}
	}
	if len(installed) > 0 {
		return "", fmt.Errorf("server version %d is newer than the newest client (%d)", major, installed[len(installed)-1])
	}
	return exec.LookPath(name)
}

func (p *postgres) Backup(ctx context.Context, dir string) error {
	pgDump, err := p.tool(ctx, "pg_dump")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, dumpFile+".partial")
	if err := proc.Run(ctx, p.env(), pgDump, "-Fc", "-f", tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, dumpFile))
}

// Restore replaces every non-system schema with the dump so the database matches the snapshot exactly.
func (p *postgres) Restore(ctx context.Context, dir string) error {
	dump := filepath.Join(dir, dumpFile)
	if _, err := os.Stat(dump); err != nil {
		return fmt.Errorf("snapshot has no database dump for %s", p.cfg.Name)
	}
	pgRestore, err := p.tool(ctx, "pg_restore")
	if err != nil {
		return err
	}
	if err := proc.Run(ctx, p.env(), "psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-c", dropSchemas); err != nil {
		return err
	}
	return proc.Run(ctx, p.env(), pgRestore, "--no-owner", "--exit-on-error", "-d", p.cfg.Database, dump)
}

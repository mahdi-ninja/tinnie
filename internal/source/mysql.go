package source

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"tinnie/internal/config"
	"tinnie/internal/proc"
)

const sqlFile = "database.sql"

// dropObjects lists a DROP statement for every table, view, procedure, function and event in the
// current database. Triggers go with their tables.
const dropObjects = "SELECT CONCAT('DROP ', IF(TABLE_TYPE = 'VIEW', 'VIEW', 'TABLE'), ' IF EXISTS `', REPLACE(TABLE_NAME, '`', '``'), '`;')" +
	" FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE()" +
	" UNION ALL SELECT CONCAT('DROP ', ROUTINE_TYPE, ' IF EXISTS `', REPLACE(ROUTINE_NAME, '`', '``'), '`;')" +
	" FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = DATABASE() AND ROUTINE_TYPE IN ('PROCEDURE', 'FUNCTION')" +
	" UNION ALL SELECT CONCAT('DROP EVENT IF EXISTS `', REPLACE(EVENT_NAME, '`', '``'), '`;')" +
	" FROM information_schema.EVENTS WHERE EVENT_SCHEMA = DATABASE()"

type mysql struct{ cfg config.Mysql }

func (m *mysql) Name() string { return m.cfg.Name }
func (m *mysql) Type() string { return "mysql" }

func (m *mysql) Describe() string {
	return fmt.Sprintf("%s@%s:%d/%s", m.cfg.User, m.cfg.Host, m.cfg.Port, m.cfg.Database)
}

// env passes the password as MYSQL_PWD so it never appears on a command line.
func (m *mysql) env() map[string]string {
	env := map[string]string{}
	if m.cfg.Password != "" {
		env["MYSQL_PWD"] = m.cfg.Password
	}
	return env
}

// args are the connection options for mariadb and mariadb-dump. The 11.4 client verifies the server's TLS
// certificate by default and, when it cannot, adds an "insecure passwordless login" warning to every error,
// even with a password; Compose stack databases have no certificate to verify, so verification is off.
func (m *mysql) args(extra ...string) []string {
	args := []string{"--skip-ssl-verify-server-cert", "--protocol=tcp", "--host=" + m.cfg.Host, "--port=" + strconv.Itoa(m.cfg.Port), "--user=" + m.cfg.User}
	return append(args, extra...)
}

func (m *mysql) Check(ctx context.Context) error {
	return proc.RunQuiet(ctx, m.env(), "mariadb", m.args("--batch", "-e", "SELECT 1", m.cfg.Database)...)
}

func (m *mysql) Backup(ctx context.Context, dir string) error {
	tmp := filepath.Join(dir, sqlFile+".partial")
	err := proc.RunQuiet(ctx, m.env(), "mariadb-dump", m.args(
		"--single-transaction", "--quick", "--routines", "--triggers", "--events", "--no-tablespaces",
		"--default-character-set=utf8mb4", "--result-file="+tmp, m.cfg.Database)...)
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, sqlFile))
}

// Restore drops every object in the database and loads the dump, so the database matches the snapshot
// exactly. The database itself is kept, along with its grants.
func (m *mysql) Restore(ctx context.Context, dir string) error {
	dump := filepath.Join(dir, sqlFile)
	if _, err := os.Stat(dump); err != nil {
		return fmt.Errorf("snapshot has no database dump for %s", m.cfg.Name)
	}
	drops, err := proc.Output(ctx, m.env(), "mariadb", m.args("--batch", "--skip-column-names", "--raw", "-e", dropObjects, m.cfg.Database)...)
	if err != nil {
		return err
	}
	if drops != "" {
		if err := m.run(ctx, strings.NewReader("SET FOREIGN_KEY_CHECKS=0;\n"+drops+"\n")); err != nil {
			return fmt.Errorf("could not empty the database: %w", err)
		}
	}
	f, err := os.Open(dump)
	if err != nil {
		return err
	}
	defer f.Close()
	return m.run(ctx, f)
}

func (m *mysql) run(ctx context.Context, stdin io.Reader) error {
	cmd := proc.Command(ctx, m.env(), "mariadb", m.args("--batch", m.cfg.Database)...)
	cmd.Stdin = stdin
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("mariadb: %w: %s", err, msg)
		}
		return fmt.Errorf("mariadb: %w", err)
	}
	return nil
}

package config

import (
	"strings"
	"testing"
)

const valid = `
name: thrive
schedule: "0 */6 * * *"
timezone: Australia/Sydney
repository:
  url: /backups/restic
  password: secret
  keep:
    within: 24h
    daily: 7
notify:
  urls: ntfy://ntfy.sh/thrive
sources:
  - name: db
    type: postgres
    host: postgres
    database: thrive
    user: thrive
    password: pw
  - name: assets
    type: s3
    endpoint: http://storage:9000
    bucket: thrive-assets
    prefix: /uploads/
    access_key: key
    secret_key: secret
    path_style: true
`

func TestParseValid(t *testing.T) {
	cfg, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "thrive" || cfg.Schedule == nil || cfg.Schedule.Location().String() != "Australia/Sydney" {
		t.Errorf("unexpected config: %+v", cfg)
	}
	if len(cfg.Sources) != 2 {
		t.Fatalf("sources = %d", len(cfg.Sources))
	}
	if p := cfg.Sources[0].Postgres; p == nil || p.Port != 5432 {
		t.Errorf("postgres defaults not applied: %+v", p)
	}
	if s := cfg.Sources[1].S3; s == nil || s.Provider != "Other" || s.Prefix != "uploads" {
		t.Errorf("s3 defaults not applied: %+v", s)
	}
	if cfg.Repository.Keep.Daily != 7 || len(cfg.Notify.URLs) != 1 || cfg.Notify.Level != NotifyError {
		t.Errorf("repository or notify not parsed: %+v %+v", cfg.Repository, cfg.Notify)
	}
}

func TestDefaults(t *testing.T) {
	cfg, err := Parse([]byte(`
schedule: "@daily"
repository: {url: /r, password: p}
sources: [{name: db, type: postgres, host: h, database: d, user: u}]
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "tinnie" || cfg.Timezone != "UTC" || !cfg.Repository.Keep.Empty() {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestReportsEveryProblem(t *testing.T) {
	_, err := Parse([]byte(`
name: Bad Name
schedule: "not cron"
timezone: Mars/Base
repository:
  keep: {within: forever}
sources:
  - name: db
    type: postgres
    host: h
  - name: db
    type: s3
    bucket: b
    access_key: k
    secret_key: s
  - type: mongo
`))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{
		`name "Bad Name"`,
		`timezone "Mars/Base"`,
		`schedule "not cron"`,
		"repository.url is required",
		"repository.password is required",
		`keep.within "forever"`,
		"sources[0] (db): database is required",
		"sources[0] (db): user is required",
		`sources[1] (db): name "db" is used more than once`,
		"sources[2]: name is required",
		`type "mongo" is not supported (postgres, mysql, s3, volume or command)`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q:\n%v", want, err)
		}
	}
}

func TestRejectsUnknownFields(t *testing.T) {
	_, err := Parse([]byte(`
schedule: "@daily"
repository: {url: /r, password: p}
sources: [{name: db, type: postgres, host: h, database: d, user: u, passwrd: typo}]
`))
	if err == nil || !strings.Contains(err.Error(), "passwrd") {
		t.Errorf("expected unknown field error, got %v", err)
	}
	_, err = Parse([]byte("schedul: x\n"))
	if err == nil || !strings.Contains(err.Error(), "schedul") {
		t.Errorf("expected unknown top-level field error, got %v", err)
	}
}

const base = `
schedule: "@daily"
repository: {url: /r, password: p}
`

func TestSources(t *testing.T) {
	tests := []struct {
		name    string
		sources string
		want    string // error substring; empty means valid
		check   func(t *testing.T, cfg *Config)
	}{
		{
			name: "mysql defaults",
			sources: `
  - {name: db, type: mysql, host: db, database: app, user: app, password: pw}`,
			check: func(t *testing.T, cfg *Config) {
				s := cfg.Sources[0]
				if s.Mysql == nil || s.Mysql.Port != 3306 || s.Mysql.Password != "pw" {
					t.Errorf("mysql = %+v", s.Mysql)
				}
			},
		},
		{name: "mysql missing host", sources: `
  - {name: db, type: mysql, database: app, user: app}`, want: "sources[0] (db): host is required"},
		{name: "mysql missing database and user", sources: `
  - {name: db, type: mysql, host: db}`, want: "database is required\n  sources[0] (db): user is required"},
		{name: "mysql unknown field", sources: `
  - {name: db, type: mysql, host: db, database: d, user: u, sslmode: x}`, want: "sslmode"},
		{
			name: "volume",
			sources: `
  - name: wordpress
    type: volume
    path: /sources/wordpress/
    exclude: [wp-content/cache/, "**/*.log"]`,
			check: func(t *testing.T, cfg *Config) {
				s := cfg.Sources[0]
				v := s.Volume
				if v == nil || v.Path != "/sources/wordpress" || strings.Join(v.Exclude, ",") != "wp-content/cache,**/*.log" {
					t.Errorf("volume = %+v", v)
				}
			},
		},
		{name: "volume missing path", sources: `
  - {name: files, type: volume}`, want: "path is required, for example /sources/files"},
		{name: "volume relative path", sources: `
  - {name: files, type: volume, path: sources/files}`, want: `path "sources/files" must be absolute`},
		{name: "volume root path", sources: `
  - {name: files, type: volume, path: /}`, want: "path must not be /"},
		{name: "volume absolute exclude", sources: `
  - {name: files, type: volume, path: /sources/files, exclude: [/tmp]}`, want: `exclude "/tmp" must be relative`},
		{name: "volume exclude outside", sources: `
  - {name: files, type: volume, path: /sources/files, exclude: [../x]}`, want: `exclude "../x" must stay inside path`},
		{name: "volume exclude everything", sources: `
  - {name: files, type: volume, path: /sources/files, exclude: [./]}`, want: `exclude "./" would leave out the whole volume`},
		{name: "volumes overlap", sources: `
  - {name: a, type: volume, path: /sources/a}
  - {name: b, type: volume, path: /sources/a/b}`, want: "volume paths /sources/a and /sources/a/b overlap"},
		{name: "volume siblings do not overlap", sources: `
  - {name: a, type: volume, path: /sources/a}
  - {name: b, type: volume, path: /sources/ab}`},
		{
			name: "command",
			sources: `
  - {name: gitea, type: command, service: gitea, user: git, backup: gitea dump -f /dev/stdout}`,
			check: func(t *testing.T, cfg *Config) {
				s := cfg.Sources[0]
				c := s.Command
				if c == nil || c.Service != "gitea" || c.User != "git" || c.Restore != "" {
					t.Errorf("command = %+v", c)
				}
			},
		},
		{name: "command missing service and backup", sources: `
  - {name: gitea, type: command}`, want: "service is required (the Compose service to run the command in)\n  sources[0] (gitea): backup is required"},
		{name: "command bad service", sources: `
  - {name: gitea, type: command, service: "git tea", backup: x}`, want: `service "git tea" is not a valid Compose service name`},
		{
			name: "restore_policy defaults",
			sources: `
  - {name: db, type: mysql, host: db, database: app, user: app}
  - {name: gitea, type: command, service: gitea, backup: x}
  - {name: wiki, type: command, service: wiki, backup: x, restore: y}`,
			check: func(t *testing.T, cfg *Config) {
				if got := policies(cfg); got != "auto never auto" {
					t.Errorf("policies = %q", got)
				}
			},
		},
		{
			name: "restore_policy on every type",
			sources: `
  - {name: pg, type: postgres, host: pg, database: d, user: u, restore_policy: manual}
  - {name: db, type: mysql, host: db, database: d, user: u, restore_policy: never}
  - {name: assets, type: s3, bucket: b, access_key: k, secret_key: s, restore_policy: never}
  - {name: files, type: volume, path: /sources/files, restore_policy: manual}
  - {name: gitea, type: command, service: gitea, backup: x, restore: y, restore_policy: auto}
  - {name: wiki, type: command, service: wiki, backup: x, restore: y, restore_policy: never}
  - {name: blog, type: command, service: blog, backup: x, restore_policy: never}`,
			check: func(t *testing.T, cfg *Config) {
				if got := policies(cfg); got != "manual never never manual auto never never" {
					t.Errorf("policies = %q", got)
				}
			},
		},
		{name: "restore_policy unknown value", sources: `
  - {name: db, type: mysql, host: db, database: d, user: u, restore_policy: sometimes}`, want: `sources[0] (db): restore_policy "sometimes" must be auto, manual or never`},
		{name: "restore_policy manual without a restore command", sources: `
  - {name: gitea, type: command, service: gitea, backup: x, restore_policy: manual}`, want: `restore_policy "manual" needs a restore command`},
		{name: "restore_policy auto without a restore command", sources: `
  - {name: gitea, type: command, service: gitea, backup: x, restore_policy: auto}`, want: `restore_policy "auto" needs a restore command`},
		{name: "missing type", sources: `
  - {name: x}`, want: "type is required (postgres, mysql, s3, volume or command)"},
		{name: "bad type", sources: `
  - {name: x, type: redis}`, want: `type "redis" is not supported (postgres, mysql, s3, volume or command)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse([]byte(base + "sources:" + tt.sources + "\n"))
			if tt.want != "" {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("error = %v, want %q", err, tt.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

func policies(cfg *Config) string {
	var out []string
	for _, s := range cfg.Sources {
		out = append(out, s.RestorePolicy)
	}
	return strings.Join(out, " ")
}

func TestStopAndNotify(t *testing.T) {
	const src = "sources: [{name: db, type: postgres, host: h, database: d, user: u}]\n"
	tests := []struct {
		name  string
		yaml  string
		want  string
		check func(t *testing.T, c *Config)
	}{
		{name: "no stop or notify", check: func(t *testing.T, c *Config) {
			if c.NeedsDocker() || len(c.Notify.URLs) != 0 || c.Notify.Level != NotifyError {
				t.Errorf("unexpected %+v %+v", c.Stop, c.Notify)
			}
		}},
		{name: "stop shorthand", yaml: "stop: [php-fpm, nginx]\n", check: func(t *testing.T, c *Config) {
			if strings.Join(c.Stop.Backup, ",") != "php-fpm,nginx" || strings.Join(c.Stop.Restore, ",") != "php-fpm,nginx" || !c.NeedsDocker() {
				t.Errorf("stop = %+v", c.Stop)
			}
		}},
		{name: "stop map", yaml: "stop:\n  backup: [php-fpm]\n  restore: [php-fpm, nginx]\n", check: func(t *testing.T, c *Config) {
			if strings.Join(c.Stop.Backup, ",") != "php-fpm" || strings.Join(c.Stop.Restore, ",") != "php-fpm,nginx" {
				t.Errorf("stop = %+v", c.Stop)
			}
		}},
		{name: "stop map restore only", yaml: "stop: {restore: [web]}\n", check: func(t *testing.T, c *Config) {
			if len(c.Stop.Backup) != 0 || strings.Join(c.Stop.Restore, ",") != "web" {
				t.Errorf("stop = %+v", c.Stop)
			}
		}},
		{name: "stop map unknown key", yaml: "stop: {backups: [web]}\n", want: "stop: "},
		{name: "stop bad service", yaml: "stop: [\"php fpm\"]\n", want: `stop: "php fpm" is not a valid Compose service name`},
		{name: "stop duplicate", yaml: "stop: {backup: [web, web]}\n", want: `stop.backup: "web" is listed more than once`},
		{name: "stop string", yaml: "stop: web\n", want: "stop must be a list"},
		{name: "notify string", yaml: "notify: {urls: ntfy://ntfy.sh/x}\n", check: func(t *testing.T, c *Config) {
			if strings.Join(c.Notify.URLs, ",") != "ntfy://ntfy.sh/x" {
				t.Errorf("notify = %+v", c.Notify)
			}
		}},
		{name: "notify list and info", yaml: "notify:\n  urls: [ntfy://ntfy.sh/x, \"generic://h/p\"]\n  level: info\n", check: func(t *testing.T, c *Config) {
			if strings.Join(c.Notify.URLs, ",") != "ntfy://ntfy.sh/x,generic://h/p" || c.Notify.Level != NotifyInfo {
				t.Errorf("notify = %+v", c.Notify)
			}
		}},
		{name: "notify valid urls", yaml: "notify:\n  urls:\n    - smtp://u:p@smtp.example.com:587/?from=a@example.com&to=b@example.com\n    - generic+https://example.com/hook\n    - generic://webhook:8080/notify?disabletls=yes&template=json\n", check: func(t *testing.T, c *Config) {
			if len(c.Notify.URLs) != 3 {
				t.Errorf("notify = %+v", c.Notify)
			}
		}},
		{name: "notify unknown service", yaml: "notify: {urls: [ntfy://ntfy.sh/x, https://example.com/hook]}\n", want: `notify.urls[1]: unknown service: "https"`},
		{name: "notify bad url", yaml: "notify: {urls: [\"telegram://bad\"]}\n", want: "notify.urls[0]: telegram: invalid telegram token"},
		{name: "notify bad level", yaml: "notify: {urls: x://y, level: debug}\n", want: `notify.level "debug" must be error (only failures) or info (successes too)`},
		{name: "notify urls map", yaml: "notify: {urls: {a: b}}\n", want: "notify.urls must be a URL or a list of URLs"},
		{name: "notify unknown key", yaml: "notify: {url: x://y}\n", want: "field url not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse([]byte(base + tt.yaml + src))
			if tt.want != "" {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("error = %v, want %q", err, tt.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}

	for _, tt := range []struct{ yaml, want string }{
		{"stop: [gitea]\nsources: [{name: g, type: command, service: gitea, backup: x}]\n",
			`sources[0] (g): service "gitea" is also in stop.backup, so its backup command could not run`},
		{"stop: {restore: [gitea]}\nsources: [{name: g, type: command, service: gitea, backup: x, restore: y}]\n",
			`sources[0] (g): service "gitea" is also in stop.restore, so its restore command could not run`},
		{"stop: {restore: [gitea]}\nsources: [{name: g, type: command, service: gitea, backup: x}]\n", ""},
		{"stop: [db]\nsources: [{name: shop, type: mysql, host: db, database: d, user: u}]\n",
			`sources[0] (shop): host "db" is also in stop.backup, so the dump could not connect`},
		{"stop: {backup: [web, pg]}\nsources: [{name: app, type: postgres, host: pg, database: d, user: u}]\n",
			`sources[0] (app): host "pg" is also in stop.backup, so the dump could not connect`},
		{"stop: {restore: [db]}\nsources: [{name: shop, type: mysql, host: db, database: d, user: u}]\n",
			`sources[0] (shop): host "db" is also in stop.restore, so the restore could not connect`},
		{"stop: [web]\nsources: [{name: shop, type: mysql, host: db.example.com, database: d, user: u}]\n", ""},
		{"stop: [web]\nsources: [{name: g, type: command, service: gitea, backup: x, restore: y}]\n", ""},
	} {
		_, err := Parse([]byte(base + tt.yaml))
		if tt.want == "" && err != nil {
			t.Errorf("%q: %v", tt.yaml, err)
		}
		if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
			t.Errorf("%q: error = %v, want %q", tt.yaml, err, tt.want)
		}
	}

	cfg, err := Parse([]byte(base + "sources: [{name: g, type: command, service: gitea, backup: x}]\n"))
	if err != nil || !cfg.NeedsDocker() {
		t.Errorf("command source should need docker: %v", err)
	}
}

func TestNeverRestoredSourcesMayBeStoppedForRestore(t *testing.T) {
	parse := func(policy string) error {
		_, err := Parse([]byte(base + "stop: {restore: [db, gitea]}\nsources:\n" +
			"  - {name: db, type: mysql, host: db, database: d, user: u, restore_policy: " + policy + "}\n" +
			"  - {name: gitea, type: command, service: gitea, backup: x, restore: y, restore_policy: " + policy + "}\n"))
		return err
	}
	if err := parse("never"); err != nil {
		t.Errorf("never: %v", err)
	}
	err := parse("manual")
	for _, want := range []string{
		`sources[0] (db): host "db" is also in stop.restore`,
		`sources[1] (gitea): service "gitea" is also in stop.restore`,
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("manual: error = %v, want %q", err, want)
		}
	}
}

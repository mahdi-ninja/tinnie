package config

import (
	"strings"
	"testing"
)

const validServer = `
url: https://backups.example.com/
timezone: Australia/Sydney
max_size: 180G
retention:
  schedule: "0 4 * * *"
  keep: { within: 24h, daily: 7, weekly: 4 }
notify:
  urls: ntfy://ntfy.sh/topic
stacks:
  - name: mysite
    password: rest-pw
    backup_password: backup-pw
  - name: shop
    password: shop-rest
    keep: { daily: 30 }
  - name: blog
    password: blog-rest
    backup_password: blog-backup
    keep: { monthly: 3 }
`

func TestParseServer(t *testing.T) {
	cfg, err := ParseServer([]byte(validServer))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.URL != "https://backups.example.com" {
		t.Errorf("url %q: trailing slash should be trimmed", cfg.URL)
	}
	if cfg.MaxSize != 180<<30 {
		t.Errorf("max_size = %d", cfg.MaxSize)
	}
	if cfg.Retention.Cron != "0 4 * * *" || cfg.Retention.Schedule == nil {
		t.Errorf("retention schedule not parsed: %+v", cfg.Retention)
	}
	if cfg.Retention.Schedule.Location().String() != "Australia/Sydney" {
		t.Errorf("schedule location = %s", cfg.Retention.Schedule.Location())
	}
	if len(cfg.Notify.URLs) != 1 || cfg.Notify.Level != NotifyError {
		t.Errorf("notify = %+v", cfg.Notify)
	}
	if len(cfg.Stacks) != 3 {
		t.Fatalf("stacks = %d", len(cfg.Stacks))
	}
	if k := cfg.Stacks[0].Keep; k.Daily != 7 || k.Weekly != 4 || k.Within != "24h" {
		t.Errorf("mysite should inherit retention.keep, got %+v", k)
	}
	if k := cfg.Stacks[1].Keep; k.Daily != 30 || k.Weekly != 0 {
		t.Errorf("shop should use its own keep, got %+v", k)
	}
	if cfg.Stacks[1].BackupPassword != "" {
		t.Error("shop has no backup_password")
	}
	if got := cfg.Stacks[0].ClientURL(cfg.URL); got != "rest:https://mysite:<password>@backups.example.com/mysite" {
		t.Errorf("client url = %s", got)
	}
	if got := cfg.Stacks[0].ClientURL(""); got != "rest:https://mysite:<password>@<host>/mysite" {
		t.Errorf("client url without url = %s", got)
	}
	if got := cfg.Stacks[0].Repository("/data"); got != "/data/mysite" {
		t.Errorf("repository = %s", got)
	}
	if _, ok := cfg.Stack("shop"); !ok {
		t.Error("Stack(shop) not found")
	}
	if _, ok := cfg.Stack("nope"); ok {
		t.Error("Stack(nope) found")
	}
}

func TestParseServerDefaults(t *testing.T) {
	cfg, err := ParseServer([]byte("stacks:\n  - name: a\n    password: p\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Timezone != "UTC" || cfg.Retention.Cron != DefaultRetentionCron || cfg.MaxSize != 0 || cfg.URL != "" {
		t.Errorf("defaults: %+v", cfg)
	}
}

func TestParseServerErrors(t *testing.T) {
	cases := map[string]string{
		"stacks must list at least one": "retention:\n  keep: { daily: 1 }\n",
		"stacks must list":              "stacks: []\n",
		"name is required":              "stacks:\n  - password: p\n",
		"password is required":          "stacks:\n  - name: a\n",
		"used more than once":           "stacks:\n  - { name: a, password: p }\n  - { name: a, password: q }\n",
		"lowercase letters":             "stacks:\n  - { name: My.Site, password: p }\n",
		"must not contain":              "stacks:\n  - { name: a, password: 'p:q' }\n",
		"no keep rules":                 "stacks:\n  - { name: a, password: p, backup_password: b }\n",
		"must look like 24h":            "retention:\n  keep: { within: soon }\nstacks:\n  - { name: a, password: p }\n",
		"must not be negative":          "stacks:\n  - { name: a, password: p, keep: { daily: -1 } }\n",
		"not a known time zone":         "timezone: Mars/Olympus\nstacks:\n  - { name: a, password: p }\n",
		"retention.schedule":            "retention:\n  schedule: never\nstacks:\n  - { name: a, password: p }\n",
		"max_size":                      "max_size: lots\nstacks:\n  - { name: a, password: p }\n",
		"must start with":               "url: backups.example.com\nstacks:\n  - { name: a, password: p }\n",
		"field surprise not found":      "surprise: 1\nstacks:\n  - { name: a, password: p }\n",
	}
	for want, text := range cases {
		_, err := ParseServer([]byte(text))
		if err == nil {
			t.Errorf("%q: expected an error", want)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v", want, err)
		}
	}
}

func TestParseSize(t *testing.T) {
	cases := map[string]int64{
		"1024":   1024,
		"1k":     1 << 10,
		"500M":   500 << 20,
		"180G":   180 << 30,
		"180GB":  180 << 30,
		"180GiB": 180 << 30,
		"1.5T":   3 << 39,
		" 2 g ":  2 << 30,
	}
	for in, want := range cases {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "lots", "0", "-1G", "1X", "1 G B"} {
		if _, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q): expected an error", in)
		}
	}
}

package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/nicholas-fedor/shoutrrr/pkg/router"
	"github.com/nicholas-fedor/shoutrrr/pkg/types"
	"go.yaml.in/yaml/v3"

	"tinnie/internal/schedule"
)

const (
	EnvConfig         = "TINNIE_CONFIG"
	EnvConfigFile     = "TINNIE_CONFIG_FILE"
	DefaultConfigFile = "/config/tinnie.yml"
)

type Config struct {
	Name       string
	Schedule   *schedule.Schedule
	Cron       string
	Timezone   string
	Repository Repository
	Notify     Notify
	Stop       Stop
	Sources    []Source
}

type Repository struct {
	URL      string            `yaml:"url"`
	Password string            `yaml:"password"`
	Env      map[string]string `yaml:"env"`
	Keep     Keep              `yaml:"keep"`
}

type Keep struct {
	Within  string `yaml:"within"`
	Last    int    `yaml:"last"`
	Hourly  int    `yaml:"hourly"`
	Daily   int    `yaml:"daily"`
	Weekly  int    `yaml:"weekly"`
	Monthly int    `yaml:"monthly"`
	Yearly  int    `yaml:"yearly"`
}

func (k Keep) Empty() bool { return k == Keep{} }

// Notify lists shoutrrr URLs. Level is "error" (only failures, the default) or "info" (successes too).
type Notify struct {
	URLs  []string
	Level string
}

const (
	NotifyError = "error"
	NotifyInfo  = "info"
)

// Stop lists the Compose services to stop around a backup and around a restore.
type Stop struct {
	Backup  []string
	Restore []string
}

// NeedsDocker reports whether the config uses anything that needs the Docker socket.
func (c *Config) NeedsDocker() bool {
	if len(c.Stop.Backup) > 0 || len(c.Stop.Restore) > 0 {
		return true
	}
	for _, s := range c.Sources {
		if s.Command != nil {
			return true
		}
	}
	return false
}

type Source struct {
	Name     string
	Type     string
	Postgres *Postgres
	S3       *S3
	Mysql    *Mysql
	Volume   *Volume
	Command  *Command
}

// SourceTypes lists every supported source type, for messages.
const SourceTypes = "postgres, mysql, s3, volume or command"

type Postgres struct {
	Name     string `yaml:"name"`
	Type     string `yaml:"type"`
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Database string `yaml:"database"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	SSLMode  string `yaml:"sslmode"`
}

type S3 struct {
	Name      string `yaml:"name"`
	Type      string `yaml:"type"`
	Endpoint  string `yaml:"endpoint"`
	Region    string `yaml:"region"`
	Bucket    string `yaml:"bucket"`
	Prefix    string `yaml:"prefix"`
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
	PathStyle bool   `yaml:"path_style"`
	Provider  string `yaml:"provider"`
}

type Mysql struct {
	Name     string `yaml:"name"`
	Type     string `yaml:"type"`
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Database string `yaml:"database"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
}

// Volume is a directory mounted into the tinnie container, backed up and restored in place.
// Exclude holds restic globs relative to Path.
type Volume struct {
	Name    string   `yaml:"name"`
	Type    string   `yaml:"type"`
	Path    string   `yaml:"path"`
	Exclude []string `yaml:"exclude"`
}

// Command runs shell commands in the container of a Compose service: Backup's stdout is saved,
// and Restore gets it back on stdin.
type Command struct {
	Name    string `yaml:"name"`
	Type    string `yaml:"type"`
	Service string `yaml:"service"`
	User    string `yaml:"user"`
	Backup  string `yaml:"backup"`
	Restore string `yaml:"restore"`
}

type raw struct {
	Name       string      `yaml:"name"`
	Schedule   string      `yaml:"schedule"`
	Timezone   string      `yaml:"timezone"`
	Repository Repository  `yaml:"repository"`
	Notify     rawNotify   `yaml:"notify"`
	Stop       yaml.Node   `yaml:"stop"`
	Sources    []yaml.Node `yaml:"sources"`
}

type rawNotify struct {
	URLs  yaml.Node `yaml:"urls"`
	Level string    `yaml:"level"`
}

var (
	namePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	withinPattern = regexp.MustCompile(`^(\d+[ymdh])+$`)
	// servicePattern is what Compose accepts as a service name.
	servicePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
)

// Load reads the config from TINNIE_CONFIG, or else from TINNIE_CONFIG_FILE (default /config/tinnie.yml).
func Load() (*Config, string, error) {
	if text := os.Getenv(EnvConfig); strings.TrimSpace(text) != "" {
		cfg, err := Parse([]byte(text))
		return cfg, EnvConfig, err
	}
	path := os.Getenv(EnvConfigFile)
	if path == "" {
		path = DefaultConfigFile
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, path, fmt.Errorf("no config: set %s or mount a file at %s", EnvConfig, path)
	}
	if err != nil {
		return nil, path, err
	}
	cfg, err := Parse(data)
	return cfg, path, err
}

func Parse(data []byte) (*Config, error) {
	var r raw
	if err := strictDecode(data, &r); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	cfg := &Config{Name: r.Name, Cron: r.Schedule, Timezone: r.Timezone, Repository: r.Repository}
	if cfg.Name == "" {
		cfg.Name = "tinnie"
	} else if !namePattern.MatchString(cfg.Name) {
		add("name %q may only contain lowercase letters, digits, - and _", cfg.Name)
	}

	loc := time.UTC
	if cfg.Timezone == "" {
		cfg.Timezone = "UTC"
	} else if l, err := time.LoadLocation(cfg.Timezone); err != nil {
		add("timezone %q is not a known time zone", cfg.Timezone)
	} else {
		loc = l
	}
	if cfg.Cron == "" {
		add("schedule is required, for example \"0 */6 * * *\"")
	} else if s, err := schedule.Parse(cfg.Cron, loc); err != nil {
		add("schedule %q: %v", cfg.Cron, err)
	} else {
		cfg.Schedule = s
	}

	if cfg.Repository.URL == "" {
		add("repository.url is required")
	}
	if cfg.Repository.Password == "" {
		add("repository.password is required")
	}
	if k := cfg.Repository.Keep; k.Within != "" && !withinPattern.MatchString(k.Within) {
		add("repository.keep.within %q must look like 24h, 7d or 1y6m", k.Within)
	}

	cfg.Notify = parseNotify(r.Notify, add)
	cfg.Stop = parseStop(&r.Stop, add)

	if len(r.Sources) == 0 {
		add("sources must list at least one source")
	}
	seen := map[string]bool{}
	for i, node := range r.Sources {
		src, errs := parseSource(&node)
		label := fmt.Sprintf("sources[%d]", i)
		if src.Name != "" {
			label += " (" + src.Name + ")"
		}
		for _, e := range errs {
			add("%s: %s", label, e)
		}
		if src.Name != "" {
			if seen[src.Name] {
				add("%s: name %q is used more than once", label, src.Name)
			}
			seen[src.Name] = true
		}
		cfg.Sources = append(cfg.Sources, src)
	}
	checkVolumeOverlap(cfg.Sources, add)
	checkStopConflicts(cfg.Sources, cfg.Stop, add)

	if len(problems) > 0 {
		return nil, errors.New("config:\n  " + strings.Join(problems, "\n  "))
	}
	return cfg, nil
}

func parseSource(node *yaml.Node) (Source, []string) {
	var head struct {
		Name string `yaml:"name"`
		Type string `yaml:"type"`
	}
	if err := node.Decode(&head); err != nil {
		return Source{}, []string{err.Error()}
	}
	src := Source{Name: head.Name, Type: head.Type}
	var errs []string
	if head.Name == "" {
		errs = append(errs, "name is required")
	} else if !namePattern.MatchString(head.Name) {
		errs = append(errs, fmt.Sprintf("name %q may only contain lowercase letters, digits, - and _", head.Name))
	}
	data, err := yaml.Marshal(node)
	if err != nil {
		return src, append(errs, err.Error())
	}
	required := func(field, value string) {
		if value == "" {
			errs = append(errs, field+" is required")
		}
	}
	switch head.Type {
	case "postgres":
		p := &Postgres{}
		if err := strictDecode(data, p); err != nil {
			return src, append(errs, err.Error())
		}
		if p.Port == 0 {
			p.Port = 5432
		}
		required("host", p.Host)
		required("database", p.Database)
		required("user", p.User)
		src.Postgres = p
	case "s3":
		s := &S3{}
		if err := strictDecode(data, s); err != nil {
			return src, append(errs, err.Error())
		}
		if s.Provider == "" {
			s.Provider = "Other"
		}
		s.Prefix = strings.Trim(s.Prefix, "/")
		required("bucket", s.Bucket)
		required("access_key", s.AccessKey)
		required("secret_key", s.SecretKey)
		src.S3 = s
	case "mysql":
		m := &Mysql{}
		if err := strictDecode(data, m); err != nil {
			return src, append(errs, err.Error())
		}
		if m.Port == 0 {
			m.Port = 3306
		}
		required("host", m.Host)
		required("database", m.Database)
		required("user", m.User)
		src.Mysql = m
	case "volume":
		v := &Volume{}
		if err := strictDecode(data, v); err != nil {
			return src, append(errs, err.Error())
		}
		switch {
		case v.Path == "":
			errs = append(errs, "path is required, for example /sources/"+head.Name)
		case !path.IsAbs(v.Path):
			errs = append(errs, fmt.Sprintf("path %q must be absolute, for example /sources/%s", v.Path, head.Name))
		case path.Clean(v.Path) == "/":
			errs = append(errs, "path must not be /; mount the volume at its own folder, for example /sources/"+head.Name)
		default:
			v.Path = path.Clean(v.Path)
		}
		for i, ex := range v.Exclude {
			clean := path.Clean(ex)
			switch {
			case strings.TrimSpace(ex) == "":
				errs = append(errs, fmt.Sprintf("exclude[%d] is empty", i))
			case path.IsAbs(ex):
				errs = append(errs, fmt.Sprintf("exclude %q must be relative to path, without a leading /", ex))
			case clean == ".":
				errs = append(errs, fmt.Sprintf("exclude %q would leave out the whole volume", ex))
			case clean == ".." || strings.HasPrefix(clean, "../"):
				errs = append(errs, fmt.Sprintf("exclude %q must stay inside path", ex))
			default:
				v.Exclude[i] = clean
			}
		}
		src.Volume = v
	case "command":
		c := &Command{}
		if err := strictDecode(data, c); err != nil {
			return src, append(errs, err.Error())
		}
		if c.Service == "" {
			errs = append(errs, "service is required (the Compose service to run the command in)")
		} else if !servicePattern.MatchString(c.Service) {
			errs = append(errs, fmt.Sprintf("service %q is not a valid Compose service name", c.Service))
		}
		required("backup", c.Backup)
		src.Command = c
	case "":
		errs = append(errs, "type is required ("+SourceTypes+")")
	default:
		errs = append(errs, fmt.Sprintf("type %q is not supported (%s)", head.Type, SourceTypes))
	}
	return src, errs
}

func parseNotify(r rawNotify, add func(string, ...any)) Notify {
	n := Notify{Level: r.Level}
	urls, err := stringList(&r.URLs)
	if err != nil {
		add("notify.urls must be a URL or a list of URLs")
	}
	for i, u := range urls {
		if strings.TrimSpace(u) == "" {
			add("notify.urls[%d] is empty", i)
			continue
		}
		u = strings.TrimSpace(u)
		if err := checkNotifyURL(u); err != nil {
			add("notify.urls[%d]: %v", i, err)
			continue
		}
		n.URLs = append(n.URLs, u)
	}
	switch n.Level {
	case "":
		n.Level = NotifyError
	case NotifyError, NotifyInfo:
	default:
		add("notify.level %q must be error (only failures) or info (successes too)", n.Level)
	}
	return n
}

// checkNotifyURL builds a shoutrrr sender for u, which parses it fully without sending anything.
func checkNotifyURL(u string) error {
	sender, err := router.NewWithOptions(nil, types.SenderOptions{})
	if err != nil {
		return err
	}
	if err := sender.AddService(u); err != nil {
		return err
	}
	return sender.Close()
}

// parseStop accepts a list (the same services for backup and restore) or a map with backup and restore lists.
func parseStop(node *yaml.Node, add func(string, ...any)) Stop {
	var st Stop
	switch {
	case node.Kind == 0, node.Kind == yaml.ScalarNode && node.Tag == "!!null":
		return st
	case node.Kind == yaml.SequenceNode:
		list, err := stringList(node)
		if err != nil {
			add("stop must be a list of Compose service names, or a map with backup and restore lists")
			return st
		}
		st.Backup = checkServices("stop", list, add)
		st.Restore = st.Backup
	case node.Kind == yaml.MappingNode:
		var m struct {
			Backup  []string `yaml:"backup"`
			Restore []string `yaml:"restore"`
		}
		data, err := yaml.Marshal(node)
		if err == nil {
			err = strictDecode(data, &m)
		}
		if err != nil {
			add("stop: %v", err)
			return st
		}
		st.Backup = checkServices("stop.backup", m.Backup, add)
		st.Restore = checkServices("stop.restore", m.Restore, add)
	default:
		add("stop must be a list of Compose service names, or a map with backup and restore lists")
	}
	return st
}

func checkServices(field string, names []string, add func(string, ...any)) []string {
	seen := map[string]bool{}
	for _, n := range names {
		switch {
		case !servicePattern.MatchString(n):
			add("%s: %q is not a valid Compose service name", field, n)
		case seen[n]:
			add("%s: %q is listed more than once", field, n)
		}
		seen[n] = true
	}
	return names
}

// stringList decodes a single string or a list of strings.
func stringList(node *yaml.Node) ([]string, error) {
	switch node.Kind {
	case 0:
		return nil, nil
	case yaml.ScalarNode:
		if node.Tag == "!!null" {
			return nil, nil
		}
		return []string{node.Value}, nil
	}
	var list []string
	err := node.Decode(&list)
	return list, err
}

// checkVolumeOverlap rejects volume paths that are the same as or inside another, since each is restored in place.
func checkVolumeOverlap(sources []Source, add func(string, ...any)) {
	var vols []*Volume
	for _, s := range sources {
		if s.Volume != nil && path.IsAbs(s.Volume.Path) && s.Volume.Path != "/" {
			vols = append(vols, s.Volume)
		}
	}
	for i, a := range vols {
		for _, b := range vols[i+1:] {
			if within(a.Path, b.Path) || within(b.Path, a.Path) {
				add("sources %s and %s: volume paths %s and %s overlap", a.Name, b.Name, a.Path, b.Path)
			}
		}
	}
}

// checkStopConflicts rejects a command source whose service is stopped while its command would have to run in
// it, and a database source whose host is a service stopped while it is dumped or restored.
func checkStopConflicts(sources []Source, st Stop, add func(string, ...any)) {
	in := func(list []string, s string) bool {
		for _, x := range list {
			if x == s {
				return true
			}
		}
		return false
	}
	for i, s := range sources {
		host := ""
		switch {
		case s.Postgres != nil:
			host = s.Postgres.Host
		case s.Mysql != nil:
			host = s.Mysql.Host
		}
		if host != "" && in(st.Backup, host) {
			add("sources[%d] (%s): host %q is also in stop.backup, so the dump could not connect", i, s.Name, host)
		}
		if host != "" && in(st.Restore, host) {
			add("sources[%d] (%s): host %q is also in stop.restore, so the restore could not connect", i, s.Name, host)
		}
		c := s.Command
		if c == nil || c.Service == "" {
			continue
		}
		if in(st.Backup, c.Service) {
			add("sources[%d] (%s): service %q is also in stop.backup, so its backup command could not run", i, s.Name, c.Service)
		}
		if c.Restore != "" && in(st.Restore, c.Service) {
			add("sources[%d] (%s): service %q is also in stop.restore, so its restore command could not run", i, s.Name, c.Service)
		}
	}
}

// within reports whether p is dir or inside it.
func within(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+"/")
}

func strictDecode(data []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	return dec.Decode(out)
}

package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"tinnie/internal/schedule"
)

const (
	EnvServerConfig         = "TINNIE_SERVER_CONFIG"
	EnvServerConfigFile     = "TINNIE_SERVER_CONFIG_FILE"
	DefaultServerConfigFile = "/config/tinnie-server.yml"
	DefaultRetentionCron    = "0 3 * * *"
)

// Server is the config of tinnie-server: a rest-server for several tinnie stacks, with retention run here
// because the stacks themselves may only append.
type Server struct {
	URL         string
	Timezone    string
	MaxSize     int64 // bytes; 0 is unlimited
	MaxSizeText string
	Retention   Retention
	Notify      Notify
	Stacks      []Stack
}

type Retention struct {
	Schedule *schedule.Schedule
	Cron     string
	Keep     Keep
}

// Stack is one tinnie client: Password lets it through rest-server, BackupPassword opens its repository for
// retention. Keep is the effective rule for this stack (its own, or retention.keep).
type Stack struct {
	Name           string
	Password       string
	BackupPassword string
	Keep           Keep
}

// Repository is the restic repository path of the stack under data.
func (s Stack) Repository(data string) string { return data + "/" + s.Name }

// ClientURL is the repository URL a tinnie stack puts in its config, with the password left out.
func (s Stack) ClientURL(base string) string {
	if base == "" {
		base = "https://<host>"
	}
	return fmt.Sprintf("rest:%s://%s:<password>@%s/%s", scheme(base), s.Name, strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://"), s.Name)
}

func scheme(url string) string {
	if strings.HasPrefix(url, "http://") {
		return "http"
	}
	return "https"
}

type rawServer struct {
	URL       string       `yaml:"url"`
	Timezone  string       `yaml:"timezone"`
	MaxSize   string       `yaml:"max_size"`
	Retention rawRetention `yaml:"retention"`
	Notify    rawNotify    `yaml:"notify"`
	Stacks    []rawStack   `yaml:"stacks"`
}

type rawRetention struct {
	Schedule string `yaml:"schedule"`
	Keep     Keep   `yaml:"keep"`
}

type rawStack struct {
	Name           string `yaml:"name"`
	Password       string `yaml:"password"`
	BackupPassword string `yaml:"backup_password"`
	Keep           Keep   `yaml:"keep"`
}

var sizePattern = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([kKmMgGtT]?)[iI]?[bB]?$`)

// LoadServer reads the config from TINNIE_SERVER_CONFIG, or else from TINNIE_SERVER_CONFIG_FILE
// (default /config/tinnie-server.yml).
func LoadServer() (*Server, string, error) {
	if text := os.Getenv(EnvServerConfig); strings.TrimSpace(text) != "" {
		cfg, err := ParseServer([]byte(text))
		return cfg, EnvServerConfig, err
	}
	path := os.Getenv(EnvServerConfigFile)
	if path == "" {
		path = DefaultServerConfigFile
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, path, fmt.Errorf("no config: set %s or mount a file at %s", EnvServerConfig, path)
	}
	if err != nil {
		return nil, path, err
	}
	cfg, err := ParseServer(data)
	return cfg, path, err
}

func ParseServer(data []byte) (*Server, error) {
	var r rawServer
	if err := strictDecode(data, &r); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	cfg := &Server{URL: strings.TrimRight(strings.TrimSpace(r.URL), "/"), Timezone: r.Timezone, MaxSizeText: strings.TrimSpace(r.MaxSize)}
	if cfg.URL != "" && !strings.HasPrefix(cfg.URL, "https://") && !strings.HasPrefix(cfg.URL, "http://") {
		add("url %q must start with https:// or http://", cfg.URL)
	}

	loc := time.UTC
	if cfg.Timezone == "" {
		cfg.Timezone = "UTC"
	} else if l, err := time.LoadLocation(cfg.Timezone); err != nil {
		add("timezone %q is not a known time zone", cfg.Timezone)
	} else {
		loc = l
	}

	if cfg.MaxSizeText != "" {
		n, err := ParseSize(cfg.MaxSizeText)
		if err != nil {
			add("max_size %q must be a size such as 180G or 500M", cfg.MaxSizeText)
		}
		cfg.MaxSize = n
	}

	cfg.Retention.Cron = r.Retention.Schedule
	if cfg.Retention.Cron == "" {
		cfg.Retention.Cron = DefaultRetentionCron
	}
	if s, err := schedule.Parse(cfg.Retention.Cron, loc); err != nil {
		add("retention.schedule %q: %v", cfg.Retention.Cron, err)
	} else {
		cfg.Retention.Schedule = s
	}
	cfg.Retention.Keep = r.Retention.Keep
	checkKeep("retention.keep", cfg.Retention.Keep, add)

	cfg.Notify = parseNotify(r.Notify, add)

	if len(r.Stacks) == 0 {
		add("stacks must list at least one stack")
	}
	seen := map[string]bool{}
	for i, rs := range r.Stacks {
		label := fmt.Sprintf("stacks[%d]", i)
		if rs.Name != "" {
			label += " (" + rs.Name + ")"
		}
		st := Stack{Name: rs.Name, Password: rs.Password, BackupPassword: rs.BackupPassword, Keep: rs.Keep}
		switch {
		case st.Name == "":
			add("%s: name is required", label)
		case !namePattern.MatchString(st.Name):
			add("%s: name %q may only contain lowercase letters, digits, - and _", label, st.Name)
		case seen[st.Name]:
			add("%s: name %q is used more than once", label, st.Name)
		}
		seen[st.Name] = true
		if st.Password == "" {
			add("%s: password is required (what the stack puts in its repository url)", label)
		} else if strings.ContainsAny(st.Password, ":@/ \t\n") {
			add("%s: password must not contain :, @, / or spaces, since it goes in the repository url", label)
		}
		checkKeep(label+".keep", st.Keep, add)
		if st.Keep.Empty() {
			st.Keep = cfg.Retention.Keep
		}
		if st.BackupPassword != "" && st.Keep.Empty() {
			add("%s: has backup_password but no keep rules; set retention.keep or %s.keep", label, label)
		}
		cfg.Stacks = append(cfg.Stacks, st)
	}

	if len(problems) > 0 {
		return nil, errors.New("config:\n  " + strings.Join(problems, "\n  "))
	}
	return cfg, nil
}

func checkKeep(field string, k Keep, add func(string, ...any)) {
	if k.Within != "" && !withinPattern.MatchString(k.Within) {
		add("%s.within %q must look like 24h, 7d or 1y6m", field, k.Within)
	}
	for name, n := range map[string]int{"last": k.Last, "hourly": k.Hourly, "daily": k.Daily, "weekly": k.Weekly, "monthly": k.Monthly, "yearly": k.Yearly} {
		if n < 0 {
			add("%s.%s must not be negative", field, name)
		}
	}
}

// ParseSize reads sizes such as 180G, 1.5T, 500MB or 1024. Units are powers of 1024.
func ParseSize(s string) (int64, error) {
	m := sizePattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, err
	}
	switch strings.ToLower(m[2]) {
	case "k":
		n *= 1 << 10
	case "m":
		n *= 1 << 20
	case "g":
		n *= 1 << 30
	case "t":
		n *= 1 << 40
	}
	if n <= 0 || n > math.MaxInt64 {
		return 0, fmt.Errorf("size %q is out of range", s)
	}
	return int64(n), nil
}

// Stack returns the stack named name, if any.
func (c *Server) Stack(name string) (Stack, bool) {
	for _, s := range c.Stacks {
		if s.Name == name {
			return s, true
		}
	}
	return Stack{}, false
}

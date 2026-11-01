// Package docker is a small client for the parts of the Docker Engine API tinnie needs: finding the
// containers of a Compose service, stopping and starting them, and running commands in them.
package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	EnvHost     = "DOCKER_HOST"
	DefaultHost = "unix:///var/run/docker.sock"

	labelProject = "com.docker.compose.project"
	labelService = "com.docker.compose.service"
	labelOneOff  = "com.docker.compose.oneoff"
)

// Client talks to one Docker daemon. It is safe for concurrent use.
type Client struct {
	host    string // as configured, for messages
	network string
	addr    string
	http    *http.Client

	mu     sync.Mutex
	prefix string     // "/v1.47", set on first use
	me     *Container // tinnie's own container, set on first use
}

type Container struct {
	ID       string
	Name     string
	State    string // running, exited, ...
	Labels   map[string]string
	Hostname string // only set by Inspect
}

func (c Container) Running() bool { return c.State == "running" }

// FromEnv returns a client for DOCKER_HOST, or the default socket when it is unset.
func FromEnv() (*Client, error) {
	return New(os.Getenv(EnvHost))
}

// New returns a client for host, which is a DOCKER_HOST value such as unix:///var/run/docker.sock or tcp://proxy:2375.
func New(host string) (*Client, error) {
	if host == "" {
		host = DefaultHost
	}
	network, addr, err := parseHost(host)
	if err != nil {
		return nil, err
	}
	c := &Client{host: host, network: network, addr: addr}
	c.http = &http.Client{Transport: &http.Transport{
		DialContext:       c.dial,
		DisableKeepAlives: true,
	}}
	return c, nil
}

// Host is the daemon address as configured.
func (c *Client) Host() string { return c.host }

func parseHost(host string) (network, addr string, err error) {
	bad := fmt.Errorf("%s %q is not supported: use unix:///path/to/docker.sock or tcp://host:port", EnvHost, host)
	u, err := url.Parse(host)
	if err != nil {
		return "", "", bad
	}
	switch u.Scheme {
	case "unix":
		if u.Host != "" || u.Path == "" || u.Path == "/" {
			return "", "", bad
		}
		return "unix", u.Path, nil
	case "tcp":
		if u.Host == "" || (u.Path != "" && u.Path != "/") {
			return "", "", bad
		}
		if u.Port() == "" {
			return "tcp", net.JoinHostPort(u.Hostname(), "2375"), nil
		}
		return "tcp", u.Host, nil
	}
	return "", "", bad
}

func (c *Client) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, c.network, c.addr)
}

func (c *Client) unreachable(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return fmt.Errorf("cannot reach Docker at %s: mount /var/run/docker.sock into the tinnie service or set DOCKER_HOST (%v)", c.host, err)
}

// Ping checks the daemon answers, and settles the API version to use.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.apiPrefix(ctx)
	return err
}

// apiPrefix asks the daemon for its API version once and uses it for every later call.
func (c *Client) apiPrefix(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.prefix != "" {
		return c.prefix, nil
	}
	var v struct{ ApiVersion string }
	if err := c.call(ctx, http.MethodGet, "/version", nil, &v); err != nil {
		return "", err
	}
	if v.ApiVersion == "" {
		return "", fmt.Errorf("%s does not look like a Docker daemon: it reported no API version", c.host)
	}
	if olderThan(v.ApiVersion, "1.41") {
		return "", fmt.Errorf("Docker at %s is too old (API %s); tinnie needs Docker 20.10 or newer", c.host, v.ApiVersion)
	}
	c.prefix = "/v" + v.ApiVersion
	return c.prefix, nil
}

func olderThan(v, min string) bool {
	parse := func(s string) (int, int) {
		major, minor, _ := strings.Cut(s, ".")
		a, _ := strconv.Atoi(major)
		b, _ := strconv.Atoi(minor)
		return a, b
	}
	a1, b1 := parse(v)
	a2, b2 := parse(min)
	return a1 < a2 || (a1 == a2 && b1 < b2)
}

// apiError is an error response from the daemon.
type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string { return e.Message }

// Is lets errors.Is(err, ErrNotFound) match the daemon's 404 answers.
func (e *apiError) Is(target error) bool {
	return target == ErrNotFound && e.Status == http.StatusNotFound
}

// ErrNotFound matches an error for a container (or exec) that does not exist.
var ErrNotFound = errors.New("no such container")

func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// do sends a versioned request.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	prefix, err := c.apiPrefix(ctx)
	if err != nil {
		return err
	}
	return c.call(ctx, method, prefix+path, body, out)
}

func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	req, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return c.unreachable(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotModified {
		return c.responseError(req, resp)
	}
	if out != nil && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotModified {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("unexpected answer from Docker to %s %s: %v", method, req.URL.Path, err)
		}
	}
	return nil
}

func (c *Client) request(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (c *Client) responseError(req *http.Request, resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var m struct{ Message string }
	msg := strings.TrimSpace(string(data))
	if json.Unmarshal(data, &m) == nil && m.Message != "" {
		msg = m.Message
	}
	if resp.StatusCode == http.StatusForbidden {
		msg = fmt.Sprintf("Docker refused %s %s (%s); if DOCKER_HOST points at a socket proxy, allow CONTAINERS, POST and EXEC", req.Method, req.URL.Path, msg)
	}
	return &apiError{Status: resp.StatusCode, Message: msg}
}

// Inspect returns one container by id or name.
func (c *Client) Inspect(ctx context.Context, id string) (Container, error) {
	var r struct {
		ID     string `json:"Id"`
		Name   string
		State  struct{ Status string }
		Config struct {
			Hostname string
			Labels   map[string]string
		}
	}
	if err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil, &r); err != nil {
		return Container{}, err
	}
	return Container{ID: r.ID, Name: strings.TrimPrefix(r.Name, "/"), State: r.State.Status, Labels: r.Config.Labels, Hostname: r.Config.Hostname}, nil
}

// List returns every container, running or not, that has all the given labels ("key=value").
func (c *Client) List(ctx context.Context, labels ...string) ([]Container, error) {
	filters, _ := json.Marshal(map[string][]string{"label": labels})
	q := url.Values{"all": {"1"}, "filters": {string(filters)}}
	var rows []struct {
		ID     string `json:"Id"`
		Names  []string
		State  string
		Labels map[string]string
	}
	if err := c.do(ctx, http.MethodGet, "/containers/json?"+q.Encode(), nil, &rows); err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(rows))
	for _, r := range rows {
		name := r.ID
		if len(r.Names) > 0 {
			name = strings.TrimPrefix(r.Names[0], "/")
		}
		out = append(out, Container{ID: r.ID, Name: name, State: r.State, Labels: r.Labels})
	}
	return out, nil
}

// Stop stops a container, killing it if it has not stopped after timeout. Stopping a stopped container is not an error.
func (c *Client) Stop(ctx context.Context, id string, timeout time.Duration) error {
	path := fmt.Sprintf("/containers/%s/stop?t=%d", url.PathEscape(id), int(timeout.Seconds()))
	return c.do(ctx, http.MethodPost, path, nil, nil)
}

// Start starts a container. Starting a running container is not an error.
func (c *Client) Start(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil, nil)
}

// Project returns the Compose project of tinnie's own container.
func (c *Client) Project(ctx context.Context) (string, error) {
	self, err := c.Self(ctx)
	if err != nil {
		return "", err
	}
	p := self.Labels[labelProject]
	if p == "" {
		return "", fmt.Errorf("tinnie's container %s is not part of a Docker Compose project, so it cannot find services by name; stop and type: command only work in Compose stacks", self.Name)
	}
	return p, nil
}

// Self returns tinnie's own container, looked up once.
func (c *Client) Self(ctx context.Context) (Container, error) {
	c.mu.Lock()
	me := c.me
	c.mu.Unlock()
	if me != nil {
		return *me, nil
	}
	self, err := c.self(ctx)
	if err != nil {
		return Container{}, err
	}
	c.mu.Lock()
	c.me = &self
	c.mu.Unlock()
	return self, nil
}

// self finds tinnie's own container: by hostname (the short container id unless the service sets hostname:),
// then by the container id in /proc/self/cgroup or /proc/self/mountinfo.
func (c *Client) self(ctx context.Context) (Container, error) {
	hostname, _ := os.Hostname()
	var candidates []string
	if hostname != "" {
		candidates = append(candidates, hostname)
	}
	ids := map[string]bool{}
	for _, f := range []string{"/proc/self/cgroup", "/proc/self/mountinfo"} {
		if data, err := os.ReadFile(f); err == nil {
			for _, id := range containerIDs(string(data)) {
				ids[id] = true
				candidates = append(candidates, id)
			}
		}
	}
	tried := map[string]bool{}
	for _, id := range candidates {
		if tried[id] {
			continue
		}
		tried[id] = true
		ct, err := c.Inspect(ctx, id)
		// A hostname can also be another container's name, so it only counts if that container has this hostname.
		if err == nil && (ids[id] || ct.Hostname == hostname) {
			return ct, nil
		}
		if err != nil && !isNotFound(err) {
			return Container{}, err
		}
	}
	return Container{}, fmt.Errorf("cannot find tinnie's own container (hostname %q) through %s; stop and type: command need tinnie to run as a service of the same Compose stack on the same Docker host", hostname, c.host)
}

var (
	cgroupID    = regexp.MustCompile(`(?:docker[-/]|/)([0-9a-f]{64})(?:\.scope)?\b`)
	mountinfoID = regexp.MustCompile(`/containers/([0-9a-f]{64})/`)
)

// containerIDs pulls 64-character container ids out of /proc/self/cgroup or /proc/self/mountinfo text.
func containerIDs(text string) []string {
	var out []string
	for _, re := range []*regexp.Regexp{mountinfoID, cgroupID} {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			out = append(out, m[1])
		}
	}
	return out
}

// ServiceContainers returns the containers, running or not, of a service in tinnie's own Compose project.
// One-off containers from `docker compose run` are left out.
func (c *Client) ServiceContainers(ctx context.Context, service string) ([]Container, error) {
	project, err := c.Project(ctx)
	if err != nil {
		return nil, err
	}
	all, err := c.List(ctx, labelProject+"="+project, labelService+"="+service)
	if err != nil {
		return nil, err
	}
	var out []Container
	for _, ct := range all {
		if !strings.EqualFold(ct.Labels[labelOneOff], "true") {
			out = append(out, ct)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no container for service %q in Compose project %q: check the service name", service, project)
	}
	return out, nil
}

// RunningContainer returns the one running container of a service in tinnie's own Compose project.
func (c *Client) RunningContainer(ctx context.Context, service string) (Container, error) {
	project, err := c.Project(ctx)
	if err != nil {
		return Container{}, err
	}
	all, err := c.ServiceContainers(ctx, service)
	if err != nil {
		return Container{}, err
	}
	var running []Container
	for _, ct := range all {
		if ct.Running() {
			running = append(running, ct)
		}
	}
	switch len(running) {
	case 0:
		return Container{}, fmt.Errorf("no running container for service %q in Compose project %q", service, project)
	case 1:
		return running[0], nil
	}
	return Container{}, fmt.Errorf("service %q in Compose project %q has %d running containers; type: command needs exactly one", service, project, len(running))
}

// ExecOptions describes a command to run in a container. Nil Stdout or Stderr discards that output;
// a non-nil Stdin is copied to the command and then closed.
type ExecOptions struct {
	Cmd    []string
	User   string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Exec runs a command in a running container and returns its exit code once it has finished.
func (c *Client) Exec(ctx context.Context, id string, opts ExecOptions) (int, error) {
	var created struct {
		ID string `json:"Id"`
	}
	err := c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/exec", map[string]any{
		"Cmd":          opts.Cmd,
		"User":         opts.User,
		"AttachStdin":  opts.Stdin != nil,
		"AttachStdout": true,
		"AttachStderr": true,
		"Tty":          false,
	}, &created)
	if err != nil {
		return -1, err
	}
	if err := c.startExec(ctx, created.ID, opts); err != nil {
		return -1, err
	}
	return c.execExitCode(ctx, created.ID)
}

// startExec starts an exec on a hijacked connection, feeds it stdin and copies its output until it ends.
func (c *Client) startExec(ctx context.Context, execID string, opts ExecOptions) error {
	prefix, err := c.apiPrefix(ctx)
	if err != nil {
		return err
	}
	req, err := c.request(ctx, http.MethodPost, prefix+"/exec/"+url.PathEscape(execID)+"/start", map[string]any{"Detach": false, "Tty": false})
	if err != nil {
		return err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")

	conn, err := c.dial(ctx, "", "")
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return c.unreachable(err)
	}
	defer conn.Close()
	// Closing the connection is what interrupts blocked reads and writes when ctx ends.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	if err := req.Write(conn); err != nil {
		return c.streamErr(ctx, err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return c.streamErr(ctx, err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return c.responseError(req, resp)
	}

	stdinErr := make(chan error, 1)
	if opts.Stdin != nil {
		go func() {
			_, err := io.Copy(conn, opts.Stdin)
			if cw, ok := conn.(interface{ CloseWrite() error }); ok {
				if cerr := cw.CloseWrite(); err == nil {
					err = cerr
				}
			}
			stdinErr <- err
		}()
	} else {
		stdinErr <- nil
	}

	stdout, stderr := opts.Stdout, opts.Stderr
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if err := Demux(br, stdout, stderr); err != nil {
		var we *writeError
		if errors.As(err, &we) {
			return we.err
		}
		return c.streamErr(ctx, err)
	}
	// The command has finished. A command that exits without reading all its input makes the copy fail;
	// that is the command's business, so it is not an error here.
	conn.Close()
	<-stdinErr
	return nil
}

func (c *Client) streamErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("lost the connection to Docker while running the command: %w", err)
}

// execWait is how long Exec waits for the daemon to report an exec as finished once its output has ended.
var execWait = 5 * time.Second

func (c *Client) execExitCode(ctx context.Context, execID string) (int, error) {
	deadline := time.Now().Add(execWait)
	for {
		var r struct {
			Running  bool
			ExitCode int
		}
		if err := c.do(ctx, http.MethodGet, "/exec/"+url.PathEscape(execID)+"/json", nil, &r); err != nil {
			return -1, err
		}
		if !r.Running {
			return r.ExitCode, nil
		}
		if time.Now().After(deadline) {
			return -1, fmt.Errorf("the command is still running after %d seconds", int(execWait.Seconds()))
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Demux splits Docker's multiplexed output stream into stdout and stderr. Each frame is an 8-byte header,
// [stream, 0, 0, 0, size (4 bytes, big endian)], followed by size bytes; stream is 1 for stdout and 2 for stderr.
// An error writing to stdout or stderr is returned as it is; any other error is a broken stream.
func Demux(r io.Reader, stdout, stderr io.Writer) error {
	out, errw := &trackWriter{w: stdout}, &trackWriter{w: stderr}
	var header [8]byte
	for {
		if _, err := io.ReadFull(r, header[:]); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		size := int64(header[4])<<24 | int64(header[5])<<16 | int64(header[6])<<8 | int64(header[7])
		var w io.Writer
		switch header[0] {
		case 0, 1:
			w = out
		case 2:
			w = errw
		default:
			return fmt.Errorf("unexpected stream type %d in Docker output", header[0])
		}
		n, err := io.CopyN(w, r, size)
		if err != nil {
			if out.err != nil || errw.err != nil {
				return &writeError{errors.Join(out.err, errw.err)}
			}
			if err == io.EOF && n < size {
				return io.ErrUnexpectedEOF
			}
			return err
		}
	}
}

type writeError struct{ err error }

func (e *writeError) Error() string { return e.err.Error() }
func (e *writeError) Unwrap() error { return e.err }

// trackWriter remembers a write error so Demux can tell it from a read error.
type trackWriter struct {
	w   io.Writer
	err error
}

func (t *trackWriter) Write(p []byte) (int, error) {
	n, err := t.w.Write(p)
	if err != nil {
		t.err = err
	}
	return n, err
}

package docker

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseHost(t *testing.T) {
	tests := []struct {
		host, network, addr string
		ok                  bool
	}{
		{"unix:///var/run/docker.sock", "unix", "/var/run/docker.sock", true},
		{"unix:///run/user/1000/docker.sock", "unix", "/run/user/1000/docker.sock", true},
		{"tcp://socket-proxy:2375", "tcp", "socket-proxy:2375", true},
		{"tcp://10.0.0.5", "tcp", "10.0.0.5:2375", true},
		{"tcp://[::1]:2375", "tcp", "[::1]:2375", true},
		{"unix://", "", "", false},
		{"unix://relative.sock", "", "", false},
		{"tcp://", "", "", false},
		{"tcp://proxy:2375/path", "", "", false},
		{"ssh://me@host", "", "", false},
		{"npipe:////./pipe/docker_engine", "", "", false},
		{"/var/run/docker.sock", "", "", false},
	}
	for _, tt := range tests {
		network, addr, err := parseHost(tt.host)
		if (err == nil) != tt.ok || network != tt.network || addr != tt.addr {
			t.Errorf("parseHost(%q) = %q, %q, %v", tt.host, network, addr, err)
		}
		if err != nil && !strings.Contains(err.Error(), "unix:///path/to/docker.sock") {
			t.Errorf("parseHost(%q) error does not say what works: %v", tt.host, err)
		}
	}
	c, err := New("")
	if err != nil || c.network != "unix" || c.addr != "/var/run/docker.sock" {
		t.Errorf("default host = %+v, %v", c, err)
	}
}

func frame(stream byte, s string) []byte {
	h := make([]byte, 8, 8+len(s))
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(s)))
	return append(h, s...)
}

func TestDemux(t *testing.T) {
	big := strings.Repeat("x", 70000)
	var in bytes.Buffer
	in.Write(frame(1, "hello "))
	in.Write(frame(2, "warning\n"))
	in.Write(frame(1, "world\n"))
	in.Write(frame(1, ""))
	in.Write(frame(2, big))
	var out, errOut bytes.Buffer
	if err := Demux(&in, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if out.String() != "hello world\n" || errOut.String() != "warning\n"+big {
		t.Errorf("stdout %q, stderr %d bytes", out.String(), errOut.Len())
	}

	cut := frame(1, "truncated")
	if err := Demux(bytes.NewReader(cut[:len(cut)-3]), io.Discard, io.Discard); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("truncated payload: %v", err)
	}
	if err := Demux(bytes.NewReader(cut[:5]), io.Discard, io.Discard); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("truncated header: %v", err)
	}
	if err := Demux(bytes.NewReader([]byte("raw tty output")), io.Discard, io.Discard); err == nil {
		t.Error("raw stream should be rejected")
	}

	full := errors.New("disk full")
	err := Demux(bytes.NewReader(frame(1, "data")), failWriter{full}, io.Discard)
	var we *writeError
	if !errors.As(err, &we) || !errors.Is(err, full) {
		t.Errorf("write error = %v", err)
	}
}

type failWriter struct{ err error }

func (f failWriter) Write([]byte) (int, error) { return 0, f.err }

func TestContainerIDs(t *testing.T) {
	id := strings.Repeat("ab12", 16)
	cgroupV1 := "12:pids:/docker/" + id + "\n11:memory:/docker/" + id + "\n"
	systemd := "0::/system.slice/docker-" + id + ".scope\n"
	mountinfo := "1234 1200 254:1 /var/lib/docker/containers/" + id + "/hostname /etc/hostname rw,relatime - ext4 /dev/vda1 rw\n"
	for name, text := range map[string]string{"cgroup v1": cgroupV1, "systemd": systemd, "mountinfo": mountinfo} {
		got := containerIDs(text)
		if len(got) == 0 || got[0] != id {
			t.Errorf("%s: got %v", name, got)
		}
	}
	if got := containerIDs("0::/\n"); len(got) != 0 {
		t.Errorf("cgroup v2 without id: %v", got)
	}
}

// fakeDaemon serves the few endpoints the client uses, for one project "shop" holding tinnie and php-fpm.
func fakeDaemon(t *testing.T) (*Client, *[]string) {
	t.Helper()
	var calls []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"ApiVersion": "1.47"})
	})
	mux.HandleFunc("GET /v1.47/containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "self" {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"message": "No such container: " + r.PathValue("id")})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"Id": "self", "Name": "/shop-backup-1", "State": map[string]any{"Status": "running"},
			"Config": map[string]any{"Hostname": "self", "Labels": map[string]string{labelProject: "shop"}},
		})
	})
	mux.HandleFunc("GET /v1.47/containers/json", func(w http.ResponseWriter, r *http.Request) {
		var f map[string][]string
		json.Unmarshal([]byte(r.URL.Query().Get("filters")), &f)
		rows := []map[string]any{}
		if strings.Join(f["label"], ",") == labelProject+"=shop,"+labelService+"=php-fpm" {
			rows = append(rows,
				map[string]any{"Id": "a1", "Names": []string{"/shop-php-fpm-1"}, "State": "running", "Labels": map[string]string{}},
				map[string]any{"Id": "a2", "Names": []string{"/shop-php-fpm-run-x"}, "State": "running", "Labels": map[string]string{labelOneOff: "True"}},
			)
		}
		json.NewEncoder(w).Encode(rows)
	})
	mux.HandleFunc("POST /v1.47/containers/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.PathValue("action")+" "+r.PathValue("id")+" "+r.URL.RawQuery)
		if r.PathValue("id") == "denied" {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, "Forbidden")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1.47/exec/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"Running": r.PathValue("id") == "slow", "ExitCode": 4})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := New("tcp://" + srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return c, &calls
}

func TestClient(t *testing.T) {
	ctx := context.Background()
	c, calls := fakeDaemon(t)
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if c.prefix != "/v1.47" {
		t.Errorf("prefix = %q", c.prefix)
	}
	self, err := c.Inspect(ctx, "self")
	if err != nil || self.Name != "shop-backup-1" || self.Labels[labelProject] != "shop" {
		t.Fatalf("inspect = %+v, %v", self, err)
	}
	if _, err := c.Inspect(ctx, "nope"); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "No such container") {
		t.Errorf("missing container: %v", err)
	}

	c.me = &Container{ID: "self", Labels: map[string]string{labelProject: "shop"}}
	ct, err := c.RunningContainer(ctx, "php-fpm")
	if err != nil || ct.ID != "a1" || ct.Name != "shop-php-fpm-1" {
		t.Errorf("running container = %+v, %v", ct, err)
	}
	_, err = c.RunningContainer(ctx, "phpfpm")
	if err == nil || err.Error() != `no container for service "phpfpm" in Compose project "shop": check the service name` {
		t.Errorf("unknown service: %v", err)
	}

	if err := c.Stop(ctx, "a1", 30*time.Second); err != nil {
		t.Error(err)
	}
	if err := c.Start(ctx, "a1"); err != nil {
		t.Error(err)
	}
	if got := strings.Join(*calls, "|"); got != "stop a1 t=30|start a1 " {
		t.Errorf("calls = %q", got)
	}
	if err := c.Start(ctx, "denied"); err == nil || !strings.Contains(err.Error(), "socket proxy") {
		t.Errorf("forbidden: %v", err)
	}
}

func TestExecExitCode(t *testing.T) {
	c, _ := fakeDaemon(t)
	ctx := context.Background()
	if code, err := c.execExitCode(ctx, "done"); code != 4 || err != nil {
		t.Errorf("finished exec = %d, %v", code, err)
	}
	defer func(d time.Duration) { execWait = d }(execWait)
	execWait = 300 * time.Millisecond
	if code, err := c.execExitCode(ctx, "slow"); code != -1 || err == nil || !strings.HasPrefix(err.Error(), "the command is still running after") {
		t.Errorf("exec still running = %d, %v", code, err)
	}
}

func TestUnreachable(t *testing.T) {
	c, err := New("unix:///nonexistent/docker.sock")
	if err != nil {
		t.Fatal(err)
	}
	err = c.Ping(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "cannot reach Docker at unix:///nonexistent/docker.sock: mount /var/run/docker.sock into the tinnie service or set DOCKER_HOST") {
		t.Errorf("unreachable: %v", err)
	}
}

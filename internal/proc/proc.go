package proc

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

func Logf(format string, args ...any) {
	fmt.Fprintf(os.Stdout, "%s %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// Command builds a command that inherits only PATH, HOME and TZ plus the given variables,
// so credentials meant for one tool never leak into another.
func Command(ctx context.Context, env map[string]string, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = baseEnv()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

func Run(ctx context.Context, env map[string]string, name string, args ...string) error {
	return wrap(name, Command(ctx, env, name, args...).Run())
}

func Output(ctx context.Context, env map[string]string, name string, args ...string) (string, error) {
	cmd := Command(ctx, env, name, args...)
	cmd.Stdout = nil
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", name, err, lastLines(strings.TrimSpace(stderr.String()), 5))
	}
	return strings.TrimSpace(string(out)), nil
}

func RunQuiet(ctx context.Context, env map[string]string, name string, args ...string) error {
	cmd := Command(ctx, env, name, args...)
	var out strings.Builder
	cmd.Stdout = io.Discard
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(out.String()); msg != "" {
			return fmt.Errorf("%s: %w: %s", name, err, lastLines(msg, 5))
		}
		return wrap(name, err)
	}
	return nil
}

func wrap(name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", name, err)
}

func baseEnv() []string {
	var env []string
	for _, k := range []string{"PATH", "HOME", "TZ", "TMPDIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

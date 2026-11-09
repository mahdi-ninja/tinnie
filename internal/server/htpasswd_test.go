package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"tinnie/internal/config"
)

func TestWriteHtpasswd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "htpasswd")
	stacks := []config.Stack{{Name: "mysite", Password: "rest-pw"}, {Name: "shop", Password: "shop-pw"}}
	if err := WriteHtpasswd(path, stacks); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %q", lines)
	}
	for i, s := range stacks {
		name, hash, ok := strings.Cut(lines[i], ":")
		if !ok || name != s.Name {
			t.Errorf("line %d = %q", i, lines[i])
			continue
		}
		if !strings.HasPrefix(hash, "$2") {
			t.Errorf("%s: not a bcrypt hash: %s", name, hash)
		}
		if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(s.Password)); err != nil {
			t.Errorf("%s: hash does not match the password: %v", name, err)
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte("wrong")) == nil {
			t.Errorf("%s: hash matches a wrong password", name)
		}
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o", info.Mode().Perm())
	}

	// Rewriting with fewer stacks drops the others.
	if err := WriteHtpasswd(path, stacks[:1]); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), "shop:") {
		t.Error("removed stack still in the file")
	}
}

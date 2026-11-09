package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"tinnie/internal/config"
)

// WriteHtpasswd writes a bcrypt htpasswd file with one line per stack, so the file matches the config
// exactly: stacks removed from the config lose access on the next start, and nothing else can be in it.
func WriteHtpasswd(path string, stacks []config.Stack) error {
	var b strings.Builder
	for _, s := range stacks {
		hash, err := bcrypt.GenerateFromPassword([]byte(s.Password), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("hash password of %s: %w", s.Name, err)
		}
		fmt.Fprintf(&b, "%s:%s\n", s.Name, hash)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

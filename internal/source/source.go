package source

import (
	"context"
	"fmt"

	"tinnie/internal/config"
)

type Source interface {
	Name() string
	Type() string
	Describe() string
	Check(ctx context.Context) error
	Backup(ctx context.Context, dir string) error
	Restore(ctx context.Context, dir string) error
}

// New returns the source for c. d is the Docker client command sources run through; it may be nil when
// the config has no command source.
func New(c config.Source, d Docker) (Source, error) {
	switch {
	case c.Postgres != nil:
		return &postgres{cfg: *c.Postgres}, nil
	case c.S3 != nil:
		return &s3{cfg: *c.S3}, nil
	case c.Mysql != nil:
		return &mysql{cfg: *c.Mysql}, nil
	case c.Volume != nil:
		return &volume{cfg: *c.Volume}, nil
	case c.Command != nil:
		if d == nil {
			return nil, fmt.Errorf("source %q needs Docker access", c.Name)
		}
		return &command{cfg: *c.Command, docker: d}, nil
	}
	return nil, fmt.Errorf("source %q has unsupported type %q", c.Name, c.Type)
}

func All(cfg *config.Config, d Docker) ([]Source, error) {
	var out []Source
	for _, c := range cfg.Sources {
		s, err := New(c, d)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

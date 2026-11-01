package source

import (
	"context"
	"fmt"
	"strings"

	"tinnie/internal/config"
	"tinnie/internal/proc"
)

type s3 struct{ cfg config.S3 }

func (s *s3) Name() string { return s.cfg.Name }
func (s *s3) Type() string { return "s3" }

func (s *s3) Describe() string {
	where := s.cfg.Bucket
	if s.cfg.Prefix != "" {
		where += "/" + s.cfg.Prefix
	}
	if s.cfg.Endpoint != "" {
		return strings.TrimRight(s.cfg.Endpoint, "/") + "/" + where
	}
	return "s3://" + where
}

func (s *s3) remote() string {
	r := "src:" + s.cfg.Bucket
	if s.cfg.Prefix != "" {
		r += "/" + s.cfg.Prefix
	}
	return r
}

func (s *s3) env() map[string]string {
	env := map[string]string{
		"RCLONE_CONFIG":                       "/dev/null",
		"RCLONE_CONFIG_SRC_TYPE":              "s3",
		"RCLONE_CONFIG_SRC_PROVIDER":          s.cfg.Provider,
		"RCLONE_CONFIG_SRC_ACCESS_KEY_ID":     s.cfg.AccessKey,
		"RCLONE_CONFIG_SRC_SECRET_ACCESS_KEY": s.cfg.SecretKey,
		"RCLONE_CONFIG_SRC_FORCE_PATH_STYLE":  fmt.Sprint(s.cfg.PathStyle),
		"RCLONE_CONFIG_SRC_NO_CHECK_BUCKET":   "true",
	}
	if s.cfg.Endpoint != "" {
		env["RCLONE_CONFIG_SRC_ENDPOINT"] = s.cfg.Endpoint
	}
	if s.cfg.Region != "" {
		env["RCLONE_CONFIG_SRC_REGION"] = s.cfg.Region
	}
	return env
}

func (s *s3) Check(ctx context.Context) error {
	return proc.RunQuiet(ctx, s.env(), "rclone", "lsf", "--max-depth", "1", s.remote())
}

func (s *s3) Backup(ctx context.Context, dir string) error {
	return proc.Run(ctx, s.env(), "rclone", "sync", "--stats-log-level", "NOTICE", "--stats", "0", s.remote(), dir)
}

// Restore makes the bucket match the snapshot exactly, deleting objects added since.
func (s *s3) Restore(ctx context.Context, dir string) error {
	return proc.Run(ctx, s.env(), "rclone", "sync", "--stats-log-level", "NOTICE", "--stats", "0", dir, s.remote())
}

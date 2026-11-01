package restic

import "regexp"

var userinfo = regexp.MustCompile(`://[^/@]+@`)

func Redact(url string) string { return userinfo.ReplaceAllString(url, "://***@") }

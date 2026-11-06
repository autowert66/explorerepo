// Package repospec parses the --repo argument into a transport target.
package repospec

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type Spec struct {
	Local   bool   // use the local git-upload-pack
	User    string // ssh
	Host    string
	Port    int
	Path    string // remote path (ssh) or absolute filesystem path (local)
	Display string // human readable
}

var scp = regexp.MustCompile(`^([A-Za-z0-9._-]+)@([A-Za-z0-9._-]+):(.+)$`)

func validPath(p string) error {
	if p == "" {
		return errors.New("empty repository path")
	}
	for _, r := range p {
		if r == '\'' || r < 0x20 || r == 0x7f {
			return fmt.Errorf("invalid character %q in repository path", r)
		}
	}
	return nil
}

func validHost(h string) error {
	if h == "" || strings.HasPrefix(h, "-") || strings.ContainsAny(h, " \t\n'\"\\$`;&|<>") {
		return fmt.Errorf("invalid host %q", h)
	}
	return nil
}

func sshPath(p string) string {
	p = strings.TrimRight(p, "/")
	p = strings.TrimSuffix(p, ".git")
	return p + ".git"
}

// Parse accepts owner/repo, host/path, git@host:path, ssh://..., file:// and local paths.
func Parse(s string) (*Spec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty repository spec")
	}
	if strings.HasPrefix(s, "-") {
		return nil, fmt.Errorf("invalid repository %q", s)
	}
	switch {
	case strings.HasPrefix(s, "file://"):
		u, err := url.Parse(s)
		if err != nil {
			return nil, err
		}
		return local(u.Path)
	case strings.HasPrefix(s, "ssh://"):
		u, err := url.Parse(s)
		if err != nil {
			return nil, err
		}
		sp := &Spec{User: u.User.Username(), Host: u.Hostname()}
		if p := u.Port(); p != "" {
			sp.Port, err = strconv.Atoi(p)
			if err != nil {
				return nil, fmt.Errorf("invalid port %q", p)
			}
		}
		sp.Path = sshPath(u.Path)
		return finish(sp)
	case strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") || s == "." || strings.HasPrefix(s, "~"):
		return local(s)
	}
	if m := scp.FindStringSubmatch(s); m != nil {
		return finish(&Spec{User: m[1], Host: m[2], Path: sshPath(m[3])})
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	first, rest, ok := strings.Cut(s, "/")
	if !ok || rest == "" {
		return nil, fmt.Errorf("invalid repository %q (want owner/repo, host/path or git@host:path)", s)
	}
	if strings.Contains(first, ".") {
		return finish(&Spec{User: "git", Host: first, Path: sshPath(rest)})
	}
	return finish(&Spec{User: "git", Host: "github.com", Path: sshPath(s)})
}

func finish(sp *Spec) (*Spec, error) {
	if err := validHost(sp.Host); err != nil {
		return nil, err
	}
	if err := validHost(sp.User); sp.User != "" && err != nil {
		return nil, fmt.Errorf("invalid user %q", sp.User)
	}
	if err := validPath(sp.Path); err != nil {
		return nil, err
	}
	sp.Display = sp.Host + "/" + strings.TrimSuffix(strings.TrimPrefix(sp.Path, "/"), ".git")
	return sp, nil
}

func local(p string) (*Spec, error) {
	if strings.HasPrefix(p, "~") {
		return nil, errors.New("~ is not expanded in --repo; use an absolute path")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return nil, err
	}
	if err := validPath(abs); err != nil {
		return nil, err
	}
	return &Spec{Local: true, Path: abs, Display: abs}, nil
}

// CacheKey is a stable, filesystem-safe identifier for ref caching.
func (s *Spec) CacheKey() string {
	h := sha1.Sum([]byte(fmt.Sprintf("%v|%s|%s|%d|%s", s.Local, s.User, s.Host, s.Port, s.Path)))
	return hex.EncodeToString(h[:8])
}

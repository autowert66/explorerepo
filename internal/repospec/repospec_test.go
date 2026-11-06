package repospec

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in               string
		local            bool
		user, host, path string
		port             int
	}{
		{"octocat/Hello-World", false, "git", "github.com", "octocat/Hello-World.git", 0},
		{"octocat/Hello-World.git", false, "git", "github.com", "octocat/Hello-World.git", 0},
		{"github.com/torvalds/linux", false, "git", "github.com", "torvalds/linux.git", 0},
		{"https://github.com/torvalds/linux/", false, "git", "github.com", "torvalds/linux.git", 0},
		{"gitlab.com/group/sub/repo", false, "git", "gitlab.com", "group/sub/repo.git", 0},
		{"git@host.example:path/to/repo.git", false, "git", "host.example", "path/to/repo.git", 0},
		{"ssh://me@host:2222/srv/repo", false, "me", "host", "/srv/repo.git", 2222},
		{"ssh://git@github.com/o/r.git", false, "git", "github.com", "/o/r.git", 0},
	}
	for _, c := range cases {
		sp, err := Parse(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if sp.Local != c.local || sp.User != c.user || sp.Host != c.host || sp.Path != c.path || sp.Port != c.port {
			t.Errorf("%s: got %+v", c.in, sp)
		}
	}
}

func TestParseLocal(t *testing.T) {
	for _, in := range []string{"/tmp/x.git", "file:///tmp/x.git"} {
		sp, err := Parse(in)
		if err != nil || !sp.Local || sp.Path != "/tmp/x.git" {
			t.Errorf("%s: %+v %v", in, sp, err)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, in := range []string{"", "justaname", "o/r'; rm -rf /", "-oProxyCommand=x/y", "git@-evil:a/b", "ssh://-oProxy@h/p"} {
		if sp, err := Parse(in); err == nil {
			t.Errorf("%q: expected error, got %+v", in, sp)
		}
	}
}

func TestCacheKeyStable(t *testing.T) {
	a, _ := Parse("o/r")
	b, _ := Parse("github.com/o/r")
	if a.CacheKey() != b.CacheKey() {
		t.Error("equivalent specs should share a cache key")
	}
}

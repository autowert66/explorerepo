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
		{"gist:03dde088884cc69b013ce8cb40acbd7e", false, "git", "gist.github.com", "03dde088884cc69b013ce8cb40acbd7e.git", 0},
		{"gist://03dde088884cc69b013ce8cb40acbd7e", false, "git", "gist.github.com", "03dde088884cc69b013ce8cb40acbd7e.git", 0},
		{"gist:1234567890abcdef1234", false, "git", "gist.github.com", "1234567890abcdef1234.git", 0},
		{"https://gist.github.com/octocat/03dde088884cc69b013ce8cb40acbd7e", false, "git", "gist.github.com", "03dde088884cc69b013ce8cb40acbd7e.git", 0},
		{"https://gist.github.com/octocat/03dde088884cc69b013ce8cb40acbd7e#file-x", false, "git", "gist.github.com", "03dde088884cc69b013ce8cb40acbd7e.git", 0},
		{"gist.github.com/03dde088884cc69b013ce8cb40acbd7e", false, "git", "gist.github.com", "03dde088884cc69b013ce8cb40acbd7e.git", 0},
		{"git@gist.github.com:03dde088884cc69b013ce8cb40acbd7e", false, "git", "gist.github.com", "03dde088884cc69b013ce8cb40acbd7e.git", 0},
		{"git@gist.github.com:03dde088884cc69b013ce8cb40acbd7e.git", false, "git", "gist.github.com", "03dde088884cc69b013ce8cb40acbd7e.git", 0},
		{"ssh://git@gist.github.com/03dde088884cc69b013ce8cb40acbd7e.git", false, "git", "gist.github.com", "03dde088884cc69b013ce8cb40acbd7e.git", 0},
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
	for _, in := range []string{"", "justaname", "o/r'; rm -rf /", "-oProxyCommand=x/y", "git@-evil:a/b", "ssh://-oProxy@h/p", "gist:", "gist:notahex", "gist:../../etc", "https://gist.github.com/octocat/notahex"} {
		if sp, err := Parse(in); err == nil {
			t.Errorf("%q: expected error, got %+v", in, sp)
		}
	}
}

func TestParseGistEquivalence(t *testing.T) {
	const id = "03dde088884cc69b013ce8cb40acbd7e"
	forms := []string{
		"gist:" + id,
		"gist://" + id,
		"gist.github.com/" + id,
		"https://gist.github.com/" + id,
		"https://gist.github.com/octocat/" + id,
		"https://gist.github.com/octocat/" + id + "#file-x",
		"git@gist.github.com:" + id,
		"git@gist.github.com:" + id + ".git",
		"ssh://git@gist.github.com/" + id + ".git",
	}
	first, err := Parse(forms[0])
	if err != nil {
		t.Fatalf("%s: %v", forms[0], err)
	}
	for _, f := range forms[1:] {
		sp, err := Parse(f)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if sp.CacheKey() != first.CacheKey() {
			t.Errorf("%s: cache key differs from %s", f, forms[0])
		}
		if sp.Display != first.Display {
			t.Errorf("%s: display %q != %q", f, sp.Display, first.Display)
		}
		if sp.User != "git" || sp.Host != "gist.github.com" || sp.Path != id+".git" {
			t.Errorf("%s: got %+v", f, sp)
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

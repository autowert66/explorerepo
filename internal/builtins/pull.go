package builtins

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/autowert66/explorerepo/internal/object"
	"github.com/autowert66/explorerepo/internal/vfs"
)

func init() {
	register("pull", "materialize a file or subtree onto the real disk (pull [-f] PATH [DEST])", pullCmd)
}

type pullItem struct {
	rel  string // slash-separated, relative to DEST
	node *vfs.Node
}

// pulledFile remembers a path this pull created, so a later name that the
// filesystem resolves to the same file can be reported instead of silently
// overwriting it. This catches case folds and Unicode normalization folds
// (APFS/HFS+ compare NFC and NFD names as equal) without a normalization
// dependency.
type pulledFile struct {
	rel  string
	info os.FileInfo
}

// Filesystem name folds only happen between siblings, so comparing within one
// directory is sufficient and avoids scanning the whole pull for every entry.
func collision(seen map[string][]pulledFile, rel, target string) error {
	st, err := os.Lstat(target)
	if err != nil {
		return nil
	}
	dir := filepath.Dir(target)
	for _, s := range seen[dir] {
		if s.rel == rel || os.SameFile(st, s.info) {
			return fmt.Errorf("%q and %q name the same file on this filesystem", s.rel, rel)
		}
	}
	return nil
}

func remember(seen map[string][]pulledFile, rel, target string) {
	if st, err := os.Lstat(target); err == nil {
		dir := filepath.Dir(target)
		seen[dir] = append(seen[dir], pulledFile{rel: rel, info: st})
	}
}

// removeStaleSymlink unlinks a symlink already at target before writing the
// entry. os.WriteFile follows such a link, so with -f a stale link (left by the
// user or an earlier pull of a different tree) would send the write outside
// DEST.
func removeStaleSymlink(target string) error {
	st, err := os.Lstat(target)
	if err != nil || st.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func badComponent(c string) error {
	return badComponentFor(c, runtime.GOOS == "windows")
}

// badComponentFor applies the name rules for one host family. windows is a
// parameter so the rules can be tested on any OS.
func badComponentFor(c string, windows bool) error {
	switch {
	case c == "" || c == "." || c == "..":
		return fmt.Errorf("refusing to write path component %q", c)
	case strings.EqualFold(c, ".git"):
		return fmt.Errorf("refusing to write %q entry", c)
	case strings.ContainsAny(c, "/\x00"):
		return fmt.Errorf("refusing to write path component %q", c)
	case windows && strings.ContainsRune(c, '\\'):
		// A backslash is a legal POSIX filename byte but a path separator on
		// Windows, where writing it would escape the destination.
		return fmt.Errorf("refusing to write path component %q", c)
	case windows && strings.ContainsRune(c, ':'):
		// ':' opens an NTFS alternate data stream or a drive-relative name.
		return fmt.Errorf("refusing to write path component %q", c)
	case windows && windowsReservedName(c):
		return fmt.Errorf("refusing to write reserved name %q", c)
	case windows && windowsShortnameDotGit(c):
		// On volumes with 8.3 names, git~1 can resolve to .git.
		return fmt.Errorf("refusing to write %q entry", c)
	case windows && (strings.HasSuffix(c, ".") || strings.HasSuffix(c, " ")):
		// Windows strips these, so distinct entries can collide.
		return fmt.Errorf("refusing to write path component %q", c)
	}
	return nil
}

// windowsReservedName reports a DOS device name, with or without an extension.
func windowsReservedName(c string) bool {
	base := c
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	base = strings.ToUpper(strings.TrimRight(base, " "))
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(base) == 4 && (base[:3] == "COM" || base[:3] == "LPT") {
		return base[3] >= '1' && base[3] <= '9'
	}
	return false
}

// windowsShortnameDotGit reports an 8.3 short name for .git, such as GIT~1.
func windowsShortnameDotGit(c string) bool {
	s := strings.ToLower(c)
	s = strings.TrimPrefix(s, ".")
	rest, ok := strings.CutPrefix(s, "git~")
	if !ok || rest == "" {
		return false
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] < '0' || rest[i] > '9' {
			return false
		}
	}
	return true
}

func caseInsensitiveFS(dir string) bool {
	f, err := os.CreateTemp(dir, ".er-case-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	defer os.Remove(name)
	base := filepath.Base(name)
	upper := filepath.Join(dir, strings.ToUpper(base))
	if upper == name {
		return false
	}
	_, err = os.Lstat(upper)
	return err == nil
}

// insideDest reports whether a symlink at rel with the given target stays within DEST.
func insideDest(rel, target string) bool {
	return insideDestFor(rel, target, runtime.GOOS == "windows")
}

// insideDestFor applies the containment rule for one host family. windows is a
// parameter so the rules can be tested on any OS.
func insideDestFor(rel, target string, windows bool) bool {
	if windows {
		if windowsAbsPath(target) {
			return false
		}
		// A backslash is a path separator on Windows, so normalize before
		// the containment check; otherwise "..\..\out" looks like one
		// segment and "\Windows" does not look rooted.
		target = strings.ReplaceAll(target, `\`, "/")
		rel = strings.ReplaceAll(rel, `\`, "/")
	}
	if strings.HasPrefix(target, "/") {
		return false
	}
	joined := path.Join(path.Dir(rel), target)
	return joined != ".." && !strings.HasPrefix(joined, "../")
}

// windowsAbsPath reports a drive-qualified (C:x) or UNC (\\server) target.
func windowsAbsPath(t string) bool {
	if strings.HasPrefix(t, `\\`) {
		return true
	}
	return len(t) >= 2 && t[1] == ':' &&
		((t[0] >= 'a' && t[0] <= 'z') || (t[0] >= 'A' && t[0] <= 'Z'))
}

func pullCmd(ctx context.Context, env *Env, args []string) int {
	p, ok := env.parse("pull", args, []Opt{{Short: 'f'}}, "")
	if !ok {
		return ExitUsage
	}
	if len(p.Args) < 1 || len(p.Args) > 2 {
		return env.usage("pull", "usage: pull [-f] PATH [DEST]")
	}
	src := p.Args[0]
	n, err := env.FS.Stat(ctx, env.Abs(src))
	if err != nil {
		env.PathErr("pull", src, err)
		return ExitFail
	}
	dest := ""
	if len(p.Args) == 2 {
		dest = p.Args[1]
	} else {
		dest = path.Base(n.Path)
		if n.Path == "/" {
			dest = env.Cfg.RepoName
			if dest == "" {
				dest = "repo"
			}
		}
		dest = "./" + dest
	}
	absDest, err := filepath.Abs(dest)
	if err != nil {
		env.Errf("pull", "%v", err)
		return ExitFail
	}
	force := p.Has("f")
	if st, err := os.Lstat(absDest); err == nil {
		if st.Mode()&os.ModeSymlink != 0 {
			env.Errf("pull", "refusing to write through %s (destination is a symlink)", dest)
			return ExitFail
		}
		if !force {
			env.Errf("pull", "%s already exists (use -f to overwrite)", dest)
			return ExitFail
		}
	}

	// Plan: collect and validate everything before touching the disk.
	var items []pullItem
	if n.IsDir() {
		err = env.Walk(ctx, n, "", WalkOpts{MaxDepth: -1}, func(e WalkEntry) error {
			if e.Depth == 0 {
				return nil
			}
			rel := strings.TrimPrefix(e.Display, "/")
			for _, c := range strings.Split(rel, "/") {
				if err := badComponent(c); err != nil {
					return err
				}
			}
			items = append(items, pullItem{rel, e.Node})
			if len(items) > env.Cfg.MaxFiles {
				return fmt.Errorf("too many entries to pull (more than --max-files %d)", env.Cfg.MaxFiles)
			}
			return nil
		})
		if err != nil {
			if isFSError(err) {
				env.PathErr("pull", src, err)
			} else {
				env.Errf("pull", "%v", err)
			}
			return ExitFail
		}
	} else {
		items = []pullItem{{"", n}}
	}

	if err := checkDestAncestors(absDest); err != nil {
		env.Errf("pull", "%v", err)
		return ExitFail
	}
	parent := filepath.Dir(absDest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		env.Errf("pull", "%v", err)
		return ExitFail
	}
	if caseInsensitiveFS(parent) {
		seen := map[string]string{}
		for _, it := range items {
			k := strings.ToLower(it.rel)
			if prev, dup := seen[k]; dup && prev != it.rel {
				env.Errf("pull", "%q and %q differ only in case and this filesystem is case-insensitive", prev, it.rel)
				return ExitFail
			}
			seen[k] = it.rel
		}
	}

	if n.IsDir() {
		if err := os.MkdirAll(absDest, 0o755); err != nil {
			env.Errf("pull", "%v", err)
			return ExitFail
		}
	}
	var files, total int64
	seen := map[string][]pulledFile{}
	status := ExitOK
	const chunk = 1000
	for start := 0; start < len(items); start += chunk {
		end := min(start+chunk, len(items))
		var oids []object.OID
		for _, it := range items[start:end] {
			if it.node.IsRegular() || it.node.IsSymlink() {
				oids = append(oids, it.node.OID)
			}
		}
		if err := env.Repo.Blobs(ctx, oids); err != nil {
			env.PathErr("pull", src, err)
			return ExitFail
		}
		for _, it := range items[start:end] {
			if ctx.Err() != nil {
				return ExitFail
			}
			target := absDest
			if it.rel != "" {
				target = filepath.Join(absDest, filepath.FromSlash(it.rel))
			}
			if err := checkParent(absDest, target, n.IsDir()); err != nil {
				env.Errf("pull", "%v", err)
				return ExitFail
			}
			if err := collision(seen, it.rel, target); err != nil {
				env.Errf("pull", "%v", err)
				return ExitFail
			}
			switch {
			case it.node.IsDir():
				if err := os.MkdirAll(target, 0o755); err != nil {
					env.Errf("pull", "%v", err)
					return ExitFail
				}
			case it.node.IsGitlink():
				fmt.Fprintf(env.Stderr, "pull: warning: skipping submodule %s (commit %s)\n", it.rel, it.node.OID)
				if err := os.MkdirAll(target, 0o755); err != nil {
					env.Errf("pull", "%v", err)
					return ExitFail
				}
			case it.node.IsSymlink():
				tb, err := env.Repo.Blob(ctx, it.node.OID)
				if err != nil {
					env.PathErr("pull", it.rel, err)
					return ExitFail
				}
				ts := string(tb)
				if n.IsDir() && insideDest(it.rel, ts) {
					os.Remove(target)
					if err := os.Symlink(ts, target); err != nil {
						env.Errf("pull", "%v", err)
						return ExitFail
					}
				} else {
					fmt.Fprintf(env.Stderr, "pull: warning: symlink %s -> %s points outside the destination; wrote a regular file containing the target\n", it.rel, ts)
					if err := removeStaleSymlink(target); err != nil {
						env.Errf("pull", "%v", err)
						return ExitFail
					}
					if err := os.WriteFile(target, tb, 0o644); err != nil {
						env.Errf("pull", "%v", err)
						return ExitFail
					}
				}
				files++
			default:
				data, err := env.Repo.Blob(ctx, it.node.OID)
				if err != nil {
					env.PathErr("pull", it.rel, err)
					return ExitFail
				}
				mode := os.FileMode(0o644)
				if it.node.IsExec() {
					mode = 0o755
				}
				if err := removeStaleSymlink(target); err != nil {
					env.Errf("pull", "%v", err)
					return ExitFail
				}
				if err := os.WriteFile(target, data, mode); err != nil {
					env.Errf("pull", "%v", err)
					return ExitFail
				}
				os.Chmod(target, mode)
				files++
				total += int64(len(data))
			}
			remember(seen, it.rel, target)
		}
	}
	env.Stdout.Printf("pulled %d files (%.1f MiB) to %s\n", files, float64(total)/(1<<20), dest)
	return status
}

// checkDestAncestors refuses a destination below a symlink or a non-directory.
// It walks every component from the volume root, so an existing directory
// deeper in the path cannot hide a symlinked ancestor. The first component
// under the root is exempt because `/var`, `/tmp`, `/bin` and similar system
// aliases are symlinks on normal systems.
func checkDestAncestors(target string) error {
	dir := filepath.Dir(target)
	vol := filepath.VolumeName(dir)
	root := vol + string(filepath.Separator)
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." {
		return nil
	}
	cur := root
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, filepath.FromSlash(part))
		st, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
			if i == 0 && st.Mode()&os.ModeSymlink != 0 {
				continue
			}
			return fmt.Errorf("refusing to write through %s (not a plain directory)", cur)
		}
	}
	return nil
}

// checkParent refuses to write through a symlink or non-directory on the way to target.
func checkParent(root, target string, isTree bool) error {
	dir := filepath.Dir(target)
	for {
		st, err := os.Lstat(dir)
		if err == nil {
			if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
				return fmt.Errorf("refusing to write through %s (not a plain directory)", dir)
			}
			return nil
		}
		if !os.IsNotExist(err) {
			return err
		}
		if dir == root || dir == filepath.Dir(dir) {
			return nil
		}
		dir = filepath.Dir(dir)
	}
}

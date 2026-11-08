package builtins

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/autowert66/explorerepo/internal/color"
	"github.com/autowert66/explorerepo/internal/store"
)

// Styler styles output for a terminal; see package color.
type Styler = color.Styler

// HelpStyled styles the text Help(args) returned. Errors and the agents line,
// which is meant to be copied into a file, stay plain.
func HelpStyled(s Styler, args []string, text string, ok bool) string {
	if !ok || len(args) > 0 && args[0] == "agents" {
		return text
	}
	return s.Help(text)
}

// Source is where explorerepo is developed and installed from.
const Source = "https://github.com/autowert66/explorerepo"

// AgentsLine is the line to add to AGENTS.md or CLAUDE.md ('help agents').
// README.md quotes it verbatim; a test keeps the two in sync.
const AgentsLine = "To read or search the source of any remote git repo (a library, a dependency, a tool you are asked about), run `explorerepo --repo ORG/REPO -c '<script>'` in your shell (it is a CLI, not an MCP tool) instead of cloning, fetching raw GitHub URLs, `gh api`, or answering from memory; " +
	"it is a read-only shell over the default branch (ls, tree, cat, head, sed -n, grep -A/-B/-C, rg, find, wc, sort, pipes, loops; " +
	"no awk). " +
	"Each call costs a network round trip, so aim for 2-3 calls: (1) orient and locate in one script, e.g. " +
	"`-c 'tree -L 2 -d; echo \"## hits\"; grep -rn -m 30 \"KEYWORD\" src --include=\"*.rs\"'`; " +
	"(2) read every relevant range at once, e.g. `-c 'sed -n \"40,120p\" src/a.rs; echo \"## b.rs\"; " +
	"grep -n -A 25 \"fn name\" src/b.rs'`; then answer. " +
	"Prefer `grep -n -A N` and `sed -n` ranges over `cat` of whole files. " +
	"`--repo` also takes gitlab.com/GROUP/REPO, gist:ID, any git URL, and `--rev TAG`; " +
	"`explorerepo help` lists everything."

// Version reports the module version stamped by the Go toolchain: the tag or
// pseudo-version for go install, a VCS pseudo-version for local builds.
func Version() string {
	v := "(devel)"
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		v = bi.Main.Version
	}
	return fmt.Sprintf("explorerepo %s (%s %s/%s)\n%s\n", v, runtime.Version(), runtime.GOOS, runtime.GOARCH, Source)
}

// Help returns the agent-facing manual, a topic, or a single command's entry.
// ok is false when the name is neither a topic nor a command, so the caller can
// turn the miss into a non-zero exit.
func Help(args []string) (string, bool) {
	if len(args) == 0 {
		return helpText, true
	}
	name := args[0]
	if len(args) > 1 {
		return fmt.Sprintf("help: unexpected argument %q after %q\n", args[1], name), false
	}
	switch name {
	case "repos":
		return helpRepos, true
	case "flags":
		return helpFlags, true
	case "agents":
		return AgentsLine + "\n", true
	case "about":
		return helpAbout(), true
	}
	if h, ok := helpDetail[name]; ok {
		return h, true
	}
	if e, ok := Lookup(name); ok {
		return fmt.Sprintf("%s: %s\nSee 'help' for the supported flags.\n", e.Name, e.Summary), true
	}
	return fmt.Sprintf("help: no such command or topic %q (topics: repos, flags, agents, about; run 'help' for the commands)\n", name), false
}

const helpRepos = `--repo SPEC forms
  owner/repo                     GitHub (github.com)
  HOST/PATH                      e.g. github.com/o/r, gitlab.com/group/sub/repo, codeberg.org/o/r
  https://HOST/PATH              any host; also git@HOST:PATH.git and ssh://USER@HOST[:PORT]/PATH
  gist:ID                        GitHub gist; also gist.github.com/ID or the gist's web URL
  file:///abs/path, ./path       a local repository
  A trailing .git is optional.

Hosts
  Works: GitHub and gists, GitLab, Bitbucket, Codeberg/Forgejo/Gitea, googlesource.com, and any
  server speaking Git protocol v2 with filtered fetch (git upload-pack, uploadpack.allowFilter).
  Refused (no filtered fetch): git.sr.ht, git.kernel.org, savannah. Protocol v0-only servers fail.
  To test your own host: explorerepo --repo SPEC --check (each transport, step by step).

Access
  ssh is tried first (your ~/.ssh/config, keys and agent). If it cannot connect, anonymous HTTPS
  is used: public repositories only, and a note is printed once a day. Private repositories need
  ssh. --https forces HTTPS, --no-https disables the fallback. Gists use host gist.github.com, so
  a 'Host github.com' block in ~/.ssh/config does not apply to them.

--rev REV
  A branch, tag (annotated tags are peeled), refs/... name, or full 40-hex commit OID; the
  default is the remote HEAD. A tag wins over a branch with the same name, like git.
  Abbreviated OIDs are rejected. 'refs' lists what exists.
`

const helpFlags = `Options come before the command or -c script.
  --repo SPEC       the repository; required unless EXPLOREREPO_REPO is set (see 'help repos').
  --rev REV         revision, alias --hash (see 'help repos' for the accepted forms).
  -c 'SCRIPT'       run SCRIPT as a shell script; otherwise the first non-flag argument is a
                    single command with no shell parsing.
  --refresh         ignore cached refs (refs are cached for 10 minutes).
  --no-cache        keep nothing on disk; refs are not cached and the HTTPS note is suppressed.
  --no-mux          disable the ssh ControlMaster reuse (one connection per invocation).
  --https           force anonymous HTTPS instead of ssh (public repositories only).
  --no-https        never fall back to HTTPS when ssh fails.
  --max-output B    cap stdout per command, with K/M/G suffixes (default 200K).
  --max-fetch B     cap the bytes fetched into one pack (default 256M).
  --max-files N     cap the files a recursive content scan may fetch (default 20000).
  --timeout D       overall wall-clock limit (default 60s); on expiry, exit 124.
  --check           test ssh and HTTPS against SPEC step by step, then exit 0 or 128.
  --color WHEN      color output for a terminal: auto (default), always, never. At a terminal it
                    styles the help, --check, ls/tree names, rg/grep matches and the log hash.
                    auto is off for pipes, agents, NO_COLOR and TERM=dumb; FORCE_COLOR forces it.
  --version         print the version and source URL, then exit.
  -h, --help [TOPIC]  print this short usage, or a topic: --help repos, --help flags, ...

Environment: see 'help about'.
`

func helpAbout() string {
	cache, err := store.DefaultDir()
	if err != nil {
		cache = "unavailable (" + err.Error() + ")"
	}
	return Version() + `
explorerepo lets an agent read or search a remote git repository without cloning it and without
a host API. It speaks Git protocol v2 over ssh or anonymous HTTPS, fetches one commit with no
trees or blobs, then only the trees and files a command touches, and runs an embedded read-only
shell over that virtual tree. Nothing on disk is written except the cache and 'pull'.

Cache    ` + cache + `
         (EXPLOREREPO_CACHE overrides; delete it to reset; refs are cached for 10 minutes)
Exit     the script's status; 2 usage error or missing --repo; 124 timeout;
         127 unknown command; 128 setup error (auth, repo not found, bad --rev);
         70 internal error
Env      EXPLOREREPO_REPO default --repo, EXPLOREREPO_DEBUG=1 log requests to stderr,
         EXPLOREREPO_QUIET silence the HTTPS-fallback note, NO_COLOR disable color
`
}

var helpDetail = map[string]string{
	"pull": `pull [-f] PATH [DEST]
  Copy a file or directory of the repository to the REAL disk (the only command that writes).
  DEST defaults to ./<basename of PATH>, relative to the real working directory
  (not the virtual 'cd'). Refuses to overwrite without -f, to write .git entries, or to
  follow symlinks that leave DEST. It also refuses a DEST that is a symlink or that sits under
  one (pass the physical path instead), and never writes through a symlink already inside DEST.
  Names the filesystem resolves to an earlier entry (case or Unicode folds) are reported rather
  than silently overwritten; a submodule becomes an empty directory. Use it, then run real
  tools (rg, cargo, ...) on the copy.
`,
	"which": `which [-a] [-s] NAME...
  Print NAME for each command the shell provides. Every command here is a builtin, so there
  is no path to print. Exit status is 1 if any NAME is unknown. 'type -p NAME' and
  'command -v NAME' report the same set (bare names). Use it to check a tool exists before
  running it; do not fall back to a worse tool because the probe printed a name, not a path.
`,
	"log": `log [-n N] [--oneline] [--decorate|--no-decorate] [REV]
  Commit history of REV (default: the opened revision), newest first. Whole repository, no path
  filtering. At a terminal the hash is colored and each commit is followed by its refs as git
  shows them: (HEAD -> branch, branches, remote branches, tag: name). The refs are plain text,
  not color: they appear whenever stdout is a terminal, or with --decorate, and never into a
  pipe. --no-decorate turns them off.
`,
}

const helpText = `explorerepo: a read-only shell over one commit of a remote git repo (no clone).
Paths are inside the repo; cwd starts at /. Only the commands below exist (no real programs run).
Cost: directory listings are cheap; recursive grep/rg/find/tree fetch every file or tree
under the path - narrow with a path, --include or -g. Output is capped (default 200K).

VIEW      cat [-n -b -s -E] FILE...      head/tail [-n N|-N|-c N] [-q -v] FILE...  (tail -n +N)
          sed -n 'A,Bp' | 'Ap' | 'A,$p' | 'Nq'  FILE     wc [-l -w -c -m]    nl [-b a|t]
          stat PATH...     ls [-a -A -l -1 -R -d -F] [PATH...]     tree [-L N -d -a -F] [PATH]
FIND      find [PATH...] [-name|-iname|-path|-ipath|-regex PAT] [-type f|d|l] [-maxdepth N]
               [-mindepth N] [-prune] [! -o ( )] [-print|-print0|-exec CMD {} ;|+]
SEARCH    grep [-rRnivwxlLcohHFEGPIaqs] [-m N] [-A|-B|-C N] [-e PAT]... PATTERN [PATH...]
               [--include=G] [--exclude=G] [--exclude-dir=G]     (RE2 regex; BRE \| \( \) \+ work)
          rg [-i -s -S -l -c -w -F -v -n -N -H -I -q -o -a] [-m N] [-A|-B|-C N] [-g GLOB]...
             [-e PAT] [--hidden] PATTERN [PATH...]      rg --files [-g GLOB] [PATH...]
TEXT      sort [-r -n -u -f -s -h] [-k N[,M]] [-t C]   uniq [-c -d -u -i]   cut -d C -f LIST | -c LIST [-s]
          tr [-d -s -c] SET1 [SET2]   xargs [-n N | -I R] [-0] [-r] CMD...   basename dirname tee
REPO      refs [PREFIX]                list branches/tags (HEAD, refs/heads/, refs/tags/ by default)
          log [-n N] [--oneline] [--decorate|--no-decorate] [REV]  history of the revision (no path filter)
          git log | git show REV:PATH | git ls-tree [-r -d --name-only] REV [PATH] | git rev-parse [REV]
          pull [-f] PATH [DEST]        copy a file/dir to the real disk (see 'help pull')
SHELL     pipes, && || ; $(...), for/while/if, globs (src/*.go), redirect to /dev/null, cd, pwd, echo,
          printf, test/[ ], read. Env: $REPO $REV $COMMIT. Not available: writes, background jobs,
          process substitution, real programs.
PROBE     type NAME... | command -v NAME... | which NAME...   report the commands above as available
          (type -p/-P, type -t, type -a, command -V, which -a also work). They print the bare name,
          since these are builtins with no path. CMD --version and CMD --help print a stub.
Notes     Symlinks pointing outside the repo error out; cd resolves symlinks, so pwd and '..' use
          the target path; submodules are empty dirs ('not fetched'); LFS files show as pointer
          text. 'ls -l' sizes are '?' when not yet downloaded.
          At a terminal, ls/tree, rg/grep and the log hash are colored and log shows refs (see 'help log').
More      help flags (command-line options)   help repos (hosts, URL forms, private repos)
          help agents (line for AGENTS.md)     help about (version, source, cache, exit codes)

Examples
  ls -la; cat README.md | head -20
  find . -name '*.go' -not -path './vendor/*' | xargs grep -ln 'TODO'
  grep -rn 'func main' --include='*.go' cmd | head
  cd src && ls | wc -l
  pull docs ./docs-copy
`

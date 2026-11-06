package builtins

import (
	"fmt"
)

// Help returns the agent-facing manual (or a single command's entry).
func Help(args []string) string {
	if len(args) > 0 {
		name := args[0]
		if h, ok := helpDetail[name]; ok {
			return h
		}
		if e, ok := Lookup(name); ok {
			return fmt.Sprintf("%s: %s\nSee 'help' for the supported flags.\n", e.Name, e.Summary)
		}
		return fmt.Sprintf("help: no such command %q (run 'help' for the list)\n", name)
	}
	return helpText
}

var helpDetail = map[string]string{
	"pull": `pull [-f] PATH [DEST]
  Copy a file or directory of the repository to the REAL disk (the only command that writes).
  DEST defaults to ./<basename of PATH>, relative to the real working directory
  (not the virtual 'cd'). Refuses to overwrite without -f, to write .git entries, or to
  follow symlinks that leave DEST. Use it, then run real tools (rg, cargo, ...) on the copy.
`,
	"which": `which [-a] [-s] NAME...
  Print NAME for each command the shell provides. Every command here is a builtin, so there
  is no path to print. Exit status is 1 if any NAME is unknown. 'type -p NAME' and
  'command -v NAME' report the same set (bare names). Use it to check a tool exists before
  running it; do not fall back to a worse tool because the probe printed a name, not a path.
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
          log [-n N] [--oneline] [REV] history of the revision (whole repo, no path filter)
          git log | git show REV:PATH | git ls-tree [-r -d --name-only] REV [PATH] | git rev-parse [REV]
          pull [-f] PATH [DEST]        copy a file/dir to the real disk (see 'help pull')
SHELL     pipes, && || ; $(...), for/while/if, globs (src/*.go), redirect to /dev/null, cd, pwd, echo,
          printf, test/[ ], read. Env: $REPO $REV $COMMIT. Not available: writes, background jobs,
          process substitution, real programs.
PROBE     type NAME... | command -v NAME... | which NAME...   report the commands above as available
          (type -p/-P, type -t, type -a, command -V, which -a also work). They print the bare name,
          since these are builtins with no path. CMD --version and CMD --help print a stub.
Notes     Symlinks pointing outside the repo error out; submodules are empty dirs ('not fetched');
          LFS files show as pointer text. 'ls -l' sizes are '?' when not yet downloaded.

Examples
  ls -la; cat README.md | head -20
  find . -name '*.go' -not -path './vendor/*' | xargs grep -ln 'TODO'
  grep -rn 'func main' --include='*.go' cmd | head
  cd src && ls | wc -l
  pull docs ./docs-copy
`

# explorerepo

explorerepo lets an AI agent read or search a remote git repository without cloning it and without
the GitHub API. Install one Go binary and add a line to your global `AGENTS.md` (or `CLAUDE.md`),
and an agent that needs a single file stops cloning the whole repo. It runs `ls`, `cat`, `head`,
`tail`, `grep`, and `find` against the remote tree, over SSH or plain HTTPS.

```sh
go install github.com/autowert66/explorerepo/cmd/explorerepo@latest
```

Add this line to `AGENTS.md` or `CLAUDE.md`:

```markdown
To read or search a remote git repo without cloning it, run `explorerepo --repo ORG/REPO -c '<ls|cat|head|tail|grep|find ...>'`; it is a read-only shell over SSH or HTTPS, and `explorerepo help` lists the commands.
```

Then agents work in place:

```sh
explorerepo --repo octocat/Hello-World -c 'ls -la; head -20 README'
explorerepo --repo github.com/torvalds/linux --rev v6.11 -c 'grep -rn "sched_tick" kernel/sched | head'
explorerepo --repo torvalds/linux ls kernel
explorerepo --repo torvalds/linux pull drivers/gpu/drm/i915 ./i915
```

## Requirements

- To install: Go 1.21 or newer. If your toolchain is older than the version in `go.mod`, `go install`
  downloads it automatically.
- To reach a remote host: the system `ssh` with a key that can reach the host, or nothing at all
  for a public repository; explorerepo falls back to anonymous HTTPS when ssh is unavailable.
- For local repositories and the test suite: `git`.

Built and tested on macOS (arm64) with Go 1.27.3 and git 2.56.0. The protocol probes recorded in
`docs/PROBES.md` were run against GitHub and local git 2.56.0.

## Install

`go install` places the binary in `$GOBIN` (default `$(go env GOPATH)/bin`); delete that file to
uninstall. From a checkout, `make build` writes it to `bin/explorerepo` instead.

## First run

```sh
explorerepo --repo octocat/Hello-World -c 'ls -la; head -5 README'
```

On success you get the repository listing and the file contents:

```
drwxr-xr-x - .
drwxr-xr-x - ..
-rw-r--r-- ? README
Hello World!
```

`ls -l` prints `?` for the size of a blob that has not been fetched yet; the size appears after the
file has been read once.

## Usage

```
explorerepo --repo SPEC [--rev REV] [options] -c 'SCRIPT'
explorerepo --repo SPEC [--rev REV] [options] CMD [ARGS...]     # single builtin, no shell parsing
explorerepo --repo SPEC [--rev REV] [options] < script.sh
explorerepo help [CMD]

options: --rev (alias --hash)  --refresh  --no-cache  --no-mux  --https  --no-https
         --max-output BYTES (200K)  --max-fetch BYTES (256M)  --max-files N (20000)  --timeout DUR (60s)
env:     EXPLOREREPO_REPO (default --repo)  EXPLOREREPO_CACHE  EXPLOREREPO_DEBUG=1  EXPLOREREPO_QUIET
```

`SPEC` accepts `owner/repo` (GitHub), `github.com/owner/repo`, `gitlab.com/group/sub/repo`,
a gist (`gist:<id>`, `gist.github.com/<id>`, or a pasted `https://gist.github.com/<user>/<id>`
URL), `https://host/owner/repo`, `git@host:path.git`, `ssh://user@host[:port]/path`,
`file:///abs/path`, or a local path.
`--rev` accepts a branch, tag (annotated tags are peeled), a `refs/...` name, or a full 40-hex
OID; abbreviated OIDs are rejected. The byte flags accept `K`, `M`, and `G` suffixes.

Gists are ordinary git repositories, so everything below works on them unchanged. One thing to know:
gists live on the separate host `gist.github.com`, so a `Host github.com` block in `~/.ssh/config`
(keys, identity files) does not apply — add a matching `Host gist.github.com` stanza if you rely on
one. A public gist is also readable over the anonymous HTTPS fallback; a secret gist needs an SSH
key (or a token) that has access.

There is no config file. `--repo` (or `EXPLOREREPO_REPO`) is the only required setting; everything
else has the default shown above.

### Transports

SSH is tried first. If a session cannot be established — no key, port 22 blocked, `ssh` not
installed — explorerepo retries the same host over anonymous HTTPS (Git smart HTTP v2, still no
GitHub API). The object cache is shared between the two, so nothing is re-fetched once a key is set
up. Anonymous HTTPS reads **public** repositories only; private ones need ssh. When the fallback is
used, a hint is printed to stderr at most once per day (recorded in the cache dir); suppress it with
`EXPLOREREPO_QUIET`, or skip the fallback entirely with `--no-https`. `--https` forces HTTPS.
`--no-cache` also suppresses the note, since there is nowhere to record the timestamp.

The exit status is the script's. Setup errors (auth, repository not found, bad revision) print
`explorerepo: ...` and exit 128. A missing `--repo` exits 2 (usage error). An unknown command exits
127, a timeout exits 124, and an unsupported option exits 2. Output beyond `--max-output` is cut off with a notice on stderr;
truncation is not an error.

### Commands

`cat head tail sed(-n ranges) wc nl stat ls tree find grep rg sort uniq cut tr xargs basename dirname
tee refs log git(log/show/ls-tree/rev-parse) pull help`, plus the shell's own builtins
(`cd pwd echo printf test [ read`, loops, `$(...)`, pipes, globs, redirects to `/dev/null`).

Run `explorerepo help` for the exact flags. An unsupported flag or command fails with exit 2 and the
list of supported options instead of producing silently wrong output.

`pull [-f] PATH [DEST]` is the only command that writes to the real disk. It copies a file or
directory to `DEST` (default `./<basename of PATH>`), refusing to overwrite without `-f` and refusing
`.git` entries and symlink escapes. Use it when you want real tools (`rg`, `cargo`, ...) on a
subtree.

## Cache and cleanup

Fetched objects and refs are cached under `$EXPLOREREPO_CACHE`, or
`os.UserCacheDir()/explorerepo` when that is unset (on macOS, `~/Library/Caches/explorerepo`).
Objects are stored as git loose objects, so you can read them with
`GIT_OBJECT_DIRECTORY=<cache>/objects git cat-file -p <oid>`. Refs are cached for 10 minutes so an
agent sees a consistent snapshot across calls; `--refresh` bypasses the ref cache, and `--no-cache`
keeps everything in memory. To reset, delete that directory; nothing else is written.

## How it works

- **Transport.** The system `ssh` binary respects `~/.ssh/config`, keys, agent, and known_hosts, and
  runs a private ControlMaster (`/tmp/explorerepo-<uid>/`) so repeated invocations reuse one
  connection. The private socket matters: a user-configured master started without
  `SendEnv=GIT_PROTOCOL` silently downgrades the session to protocol v0, so explorerepo always sets
  its own ControlPath. `--no-mux` disables multiplexing. When the SSH session cannot be established,
  the same protocol v2 runs over anonymous HTTPS smart HTTP instead; see [Transports](#transports).
- **Laziness.** A commit is fetched with `deepen 1` and `filter tree:0`, then one tree per directory
  actually entered. Recursive commands (`find`, `grep -r`, `rg`, `tree`, `ls -R`, `pull`) fetch the
  whole subtree structure in one `blob:none` request, then the needed blobs in batches of up to 1000.
- **Safety.** No real program is executed, and nothing on the host filesystem is read or written
  except by `pull`. Symlinks that leave the repository are errors; submodules appear as empty
  directories (`not fetched`); tree entries named `..`, `.`, empty, or containing `/` or NUL are
  hidden. `--max-fetch`, `--max-files`, `--max-output`, and `--timeout` bound a runaway command.

Protocol details, measured costs, and the v0-downgrade probe are in `docs/PROBES.md`. Set
`EXPLOREREPO_DEBUG=1` to log every request (command, wants, filter, bytes, ms) to stderr.

## Prompt snippet for an agent

```
You can inspect the remote repo ORG/REPO without cloning it by running:
  explorerepo --repo ORG/REPO [--rev BRANCH_OR_TAG] -c '<shell script>'
It is a read-only shell (ls, cat, head, tail, sed -n, wc, find, tree, grep, rg, sort, uniq, cut, tr,
xargs, git log/show/ls-tree, pipes, loops, globs). Directory listings are cheap; recursive grep/find
fetch every file under the path, so narrow with a path, --include or -g. Run `explorerepo help` for
the exact flag list. A GitHub gist works the same way: `--repo gist:<id>` (or the gist URL). To work
on a subtree with real tools:
  explorerepo --repo ORG/REPO pull PATH ./dest
```

## Development

```sh
make test                         # offline: builds throwaway repos with the git CLI, uses the local transport
make race
make live                         # EXPLOREREPO_LIVE=1: octocat/Hello-World and golang/go over ssh
scripts/probe.sh ssh octocat/Hello-World.git
```

## Out of scope

SHA-256 repositories; protocol v0 servers; authenticated HTTP (the anonymous HTTPS fallback reads
public repositories only); Git LFS content (pointer files are shown as-is); path-limited `log`; a
background daemon; FUSE/NFS mounts; writing or pushing anything; process substitution, background
jobs, and real program execution inside the shell.

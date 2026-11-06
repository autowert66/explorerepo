# explorerepo

A single Go binary that lets an AI agent explore a **remote git repository without cloning it and without the
GitHub API**. It speaks Git protocol v2 to `git-upload-pack` over your own SSH (`git@github.com`), fetches only the
objects a command actually needs, and exposes the snapshot as a **read-only virtual filesystem inside an embedded
POSIX shell**, so an agent can write `find ... | xargs grep ... | head` exactly like it would locally.

```sh
explorerepo --repo octocat/Hello-World -c 'ls -la; head -20 README'
explorerepo --repo github.com/torvalds/linux --rev v6.11 -c 'grep -rn "sched_tick" kernel/sched | head'
explorerepo --repo torvalds/linux ls kernel              # argv form: one command, no quoting
explorerepo --repo torvalds/linux <<'EOF2'               # script on stdin
cd kernel/sched
find . -name '*.c' | wc -l
EOF2
explorerepo --repo torvalds/linux pull drivers/gpu/drm/i915 ./i915   # materialize to real disk
```

## Install

```sh
go install ./cmd/explorerepo        # or: make build  (-> bin/explorerepo)
```

Requirements: the Go version in `go.mod` to build; at run time only the system `ssh` (with a key that can reach the host) and, for
local repositories/tests, `git`.

## Usage

```
explorerepo --repo SPEC [--rev REV] [options] -c 'SCRIPT'
explorerepo --repo SPEC [--rev REV] [options] CMD [ARGS...]     # single builtin, no shell parsing
explorerepo --repo SPEC [--rev REV] [options] < script.sh
explorerepo help [CMD]

options: --rev (alias --hash)  --refresh  --no-cache  --no-mux
         --max-output BYTES (200K)  --max-fetch BYTES (256M)  --max-files N (20000)  --timeout DUR (60s)
env:     EXPLOREREPO_REPO (default --repo)  EXPLOREREPO_CACHE  EXPLOREREPO_DEBUG=1
```

`SPEC` is `owner/repo` (GitHub), `github.com/owner/repo`, `gitlab.com/group/sub/repo`, `git@host:path.git`,
`ssh://user@host[:port]/path`, `file:///abs/path` or a local path. `--rev` accepts a branch, tag (annotated tags are
peeled), `refs/...` name or a full 40-hex OID (abbreviated OIDs are rejected). Byte flags accept `K/M/G`.

Exit status is the script's. Setup errors (auth, repo not found, bad rev) print `explorerepo: ...` and exit 128;
an unknown command exits 127; a timeout exits 124. Output beyond `--max-output` is cut off with a notice on stderr
(truncation is not an error).

### Commands

`cat head tail sed(-n ranges) wc nl stat ls tree find grep rg sort uniq cut tr xargs basename dirname tee refs log git(log/show/ls-tree/rev-parse) pull help`,
plus the shell's own builtins (`cd pwd echo printf test [ read`, loops, `$(...)`, pipes, globs, redirects to `/dev/null`).
Run `explorerepo help` for the exact supported flags; anything unsupported fails loudly (exit 2) with the list of
supported options instead of producing silently wrong output. `pull [-f] PATH [DEST]` is the only command that
writes to the real disk, and the escape hatch when you want real tools (`rg`, `cargo`, ...) on a subtree.

## How it works

- **Transport.** The system `ssh` binary (respects `~/.ssh/config`, keys, agent, known_hosts) with a *private*
  ControlMaster (`/tmp/explorerepo-<uid>/`) so repeated invocations reuse one connection (~0.3 s instead of ~1.4 s).
  The private socket matters: a user-configured master that lacks `SendEnv=GIT_PROTOCOL` silently downgrades the
  session to protocol v0 (see `docs/PROBES.md`).
- **Protocol.** v2 only: `ls-refs`, `fetch` with `done`, `deepen 1`, `filter`, `ofs-delta`, sideband demux, own
  pkt-line and packfile parser (OFS/REF deltas, trailer checksum, every object re-hashed).
- **Laziness.** Commit with `deepen 1 + filter tree:0`, then one tree per directory you actually enter
  (`filter tree:0`). Recursive commands (`find`, `grep -r`, `rg`, `tree`, `ls -R`, `pull`) fetch the whole subtree
  structure with **one** `blob:none` request, then fetch the needed blobs in batches of <= 1000 wants.
  Finite small depths (`find -maxdepth 2`, `tree -L 2`) go level by level, one request per level.
- **Cache.** Content-addressed git loose objects under `$EXPLOREREPO_CACHE` (default `os.UserCacheDir()/explorerepo`),
  shared by all repos and readable with `GIT_OBJECT_DIRECTORY=<cache>/objects git cat-file -p <oid>`; a 64 MiB LRU in
  front; refs cached for 10 minutes (`--refresh` to bypass) so an agent sees a consistent snapshot across calls.
- **Safety.** No real program is ever executed and nothing on the host filesystem is read or written except by
  `pull`. Symlinks that leave the repo are errors; submodules appear as empty directories (`not fetched`); tree
  entries named `..`, `.`, empty or containing `/` or NUL are hidden; `pull` refuses `.git` entries, symlink escapes
  and case-folding collisions. `--max-fetch`, `--max-files`, `--max-output` and `--timeout` bound a runaway command.

Set `EXPLOREREPO_DEBUG=1` to log every request (command, wants, filter, bytes, ms) to stderr; that is the quickest way
to see the batching.

## Prompt snippet for an agent

```
You can inspect the remote repo ORG/REPO without cloning it by running:
  explorerepo --repo ORG/REPO [--rev BRANCH_OR_TAG] -c '<shell script>'
It is a read-only shell (ls, cat, head, tail, sed -n, wc, find, tree, grep, rg, sort, uniq, cut, tr, xargs, git log/show/ls-tree,
pipes, loops, globs). Directory listings are cheap; recursive grep/find fetch every file under the path, so narrow with a path,
--include or -g. Run `explorerepo help` for the exact flag list. To work on a subtree with real tools:
  explorerepo --repo ORG/REPO pull PATH ./dest
```

## Development

```sh
make test                         # offline: builds throwaway repos with the git CLI, uses the local transport
make race
make live                         # EXPLOREREPO_LIVE=1: octocat/Hello-World and golang/go over ssh
scripts/probe.sh ssh octocat/Hello-World.git
```

Package map: `cmd/explorerepo` (CLI) - `internal/{repospec,transport,protocol,pktline,pack,object,store,repo,vfs,shell,builtins}`;
`internal/testutil` (fixture repos) and `internal/repotest` (opening a repo over the counting transport) are test helpers.

## Out of scope for v1

SHA-256 repositories; protocol v0 servers; HTTP(S) transport; Git LFS content (pointer files are shown as-is);
path-limited `log`; a background daemon; FUSE/NFS mounts; writing or pushing anything; process substitution,
background jobs and real program execution inside the shell.

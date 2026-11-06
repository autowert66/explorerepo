# explorerepo — implementation plan

A single Go binary that lets an AI agent explore a remote git repository **without cloning it and without the GitHub API**. It talks Git protocol v2 to `git-upload-pack` over the user's SSH (`git@github.com`), fetches only the objects a command actually needs, and exposes the repo snapshot as a **read-only virtual filesystem inside an embedded POSIX shell**, so the agent can write `find … | xargs grep … | head` like it would locally.

```sh
explorerepo --repo octocat/Hello-World -c 'ls -la; head -20 README'
explorerepo --repo github.com/torvalds/linux --rev v6.11 -c 'grep -rn "sched_tick" kernel/sched | head'
explorerepo --repo torvalds/linux ls kernel          # argv form = one command, no quoting
explorerepo --repo torvalds/linux <<'EOF'            # script on stdin
cd kernel/sched
find . -name '*.c' | wc -l
EOF
explorerepo --repo torvalds/linux pull drivers/gpu/drm/i915 ./i915   # materialize to real disk
```

---

## 1. Decisions (and why)

### 1.1 Interface: embedded shell over a virtual tree — chosen

Options considered:

| Option | Verdict |
|---|---|
| Fixed subcommands (`ls/cat/head/tail/pull`) | Too narrow. Agents compose: `find | xargs grep`, `head -n 40 a b`, loops. Every missing composition becomes a round-trip of agent confusion. Kept only as the argv sugar form. |
| FUSE / loopback-NFS mount, agent uses real tools | Real `rg`/`grep` for free, but: (a) macOS needs macFUSE or an NFS loopback hack, plus mount lifecycle; (b) the FS only sees `readdir`/`read`, so `grep -r` on a cold tree becomes **one network round trip per file** — we can't batch because we don't know intent; (c) the agent can't tell cheap from catastrophic. Rejected for v1. |
| ProxyCommand fake SSH host (`ssh git-explore …`) | Double SSH, quoting hell, and the agent will assume a real host (make, git status…). Gives nothing the embedded shell doesn't. Rejected. |
| **Embedded shell (`mvdan.cc/sh/v3/interp`) with all commands implemented in-process against a lazy git tree** | **Chosen.** Pipes, loops, globs, `$(…)`, redirects to /dev/null all work. Because *we* implement `find`/`grep`, we know the intent and can **batch fetches** (one request for every tree under a dir, one request for hundreds of blobs). Fully sandboxed: no real binaries are ever executed, nothing on the host FS is touched except by `pull`. Works identically on macOS/Linux, no mount. |

Cost of the choice: we reimplement ~20 tools with a *subset* of flags. Mitigations: (1) every unsupported flag/command fails loudly with a message listing what *is* supported, exit 2/127, never silently wrong output; (2) `pull` is the escape hatch — materialize a subtree to disk and use real tools there.

### 1.2 Transport: system `ssh` binary, not `golang.org/x/crypto/ssh`

Spawning `ssh` respects the user's `~/.ssh/config`, keys, agent, known_hosts, and gives us **ControlMaster** connection reuse across invocations for free (~250 ms handshake paid once per 5 min). x/crypto/ssh would require reimplementing all of that. Revisit only if we build a daemon.

### 1.3 No daemon in v1

A persistent **on-disk content-addressed object cache** + ControlMaster captures almost all of the benefit (warm `ls` after cold `ls` = zero network for objects). A daemon holding open `upload-pack` sessions is a possible v2 optimization; design the `Repo` type so it could live in a daemon later, but don't build it.

### 1.4 Language: Go 1.2x, minimal deps

Deps allowed: `mvdan.cc/sh/v3` (shell), standard library. Write pkt-line, protocol v2 client, and packfile parser ourselves (≈ 600–900 lines total; it's well specified and we need control over filters/streaming). Do **not** pull in go-git.

---

## 2. Protocol facts the implementation relies on

Reference specs (in git source `Documentation/`): `gitprotocol-v2.txt`, `gitprotocol-common.txt` (pkt-line), `gitformat-pack.txt`, `rev-list-options` (`--filter`).

- **SSH invocation**: `ssh -o SendEnv=GIT_PROTOCOL <user@host> "git-upload-pack '<path>'"` with `GIT_PROTOCOL=version=2` in the env. Server speaks first: capability advertisement (`version 2`, `ls-refs…`, `fetch=shallow wait-for-done filter…`, `object-format=sha1`, maybe `object-info`), terminated by flush. If the first line is not `version 2` (it's a v0 ref advertisement with `\0` caps), fail with a clear error — v0 fallback is out of scope.
- **pkt-line**: 4 hex length (includes itself) + payload; `0000` flush, `0001` delim, `0002` response-end. Max pkt 65520 bytes.
- **ls-refs**: `command=ls-refs\n`, `object-format=sha1\n`, delim, `peel\n`, `symrefs\n`, `ref-prefix …\n`…, flush. Response lines: `<oid> <refname>[ symref-target:<t>][ peeled:<oid>]`, flush.
- **fetch**: `command=fetch\n`, `object-format=sha1\n`, delim, `ofs-delta\n`, `no-progress\n`, optional `deepen 1\n`, optional `filter <spec>\n`, `want <oid>\n` × N, `done\n`, flush.
  - **Never send `thin-pack`** (we send no haves, but don't invite REF_DELTA against objects outside the pack).
  - **Always send `done`** (otherwise the server enters negotiation and waits).
  - Response sections in order, each starts with a header pkt and sections are separated by delim: optional `acknowledgments`, optional `shallow-info` (when `deepen` used; lines `shallow <oid>`), optional `wanted-refs`, optional `packfile-uris`, then `packfile` whose following pkts are sideband: byte `1` = pack data, `2` = progress (ignore / send to debug log), `3` = fatal error. Ends with flush.
- **What `want <oid>` returns** (filters measured from each wanted object):
  - commit + `deepen 1` + `filter tree:1` → commit + root tree only. (Without `deepen 1` you'd get all history.)
  - tree + `filter tree:1` → just that tree.
  - tree + `filter blob:none` → that tree and every subtree, no blobs.
  - tree + `filter blob:limit=<n>` → whole subtree, blobs ≤ n bytes.
  - blob → just the blob.
  - multiple wants in one request are fine and are the main batching tool.
- **object-info** (optional capability): `command=object-info`, delim, `size`, `oid <oid>`…, flush → `size` header line then `<oid> <size>` lines. Use for `ls -l`/`stat` sizes if advertised; otherwise sizes are unknown until the blob is fetched.
- **Pack format**: `PACK`, u32be version 2, u32be count; per object a type/size varint header (types 1 commit, 2 tree, 3 blob, 4 tag, 6 OFS_DELTA, 7 REF_DELTA); OFS_DELTA has the git "offset encoding" varint (each continuation adds 1 before shifting — see gitformat-pack), REF_DELTA has a raw 20-byte base OID; then zlib data. Trailer = SHA-1 of everything before it — verify it. Deltas **will** appear in multi-object packs (subtree fetches), so delta resolution is required.
- **Delta format**: two size varints (base size, result size), then instructions: MSB=1 copy (offset/size bytes selected by low 7 bits, size 0 means 0x10000), MSB=0 insert next N bytes (N=0 is invalid).
- **Integrity**: for every object stored, compute `sha1("<type> <len>\0" + data)`; that is its OID. When we asked for specific OIDs, verify they arrived. Trust chains commit → tree → blob, so the server is only trusted for availability.
- **Tree object**: repeated `<octal mode> <name>\0<20 raw bytes>`. Modes: `40000` dir, `100644` file, `100755` exec, `120000` symlink (blob content = target), `160000` gitlink/submodule (OID is a commit in *another* repo — never fetch).
- **Commit object**: header lines `tree <oid>`, `parent <oid>`…, `author …`, `committer …`, blank line, message.
- **Annotated tags**: use the `peeled:` OID from ls-refs. If the user passes a tag-object OID directly, fetch it, read `object <oid>`, repeat until a commit.

---

## 3. Phase 0 — probes (do first, write results to `docs/PROBES.md`)

Write `scripts/probe.sh` that hand-crafts pkt-lines with `printf` and pipes them to `GIT_PROTOCOL=version=2 ssh -o SendEnv=GIT_PROTOCOL git@github.com "git-upload-pack 'octocat/Hello-World.git'"` (and a larger repo, e.g. `golang/go`). Record:

1. Full capability advertisement (is `filter` in `fetch=`? is `object-info` present?).
2. `ls-refs` with `ref-prefix HEAD` + `symrefs` returns `symref-target:`.
3. `want <tree-oid>` + `filter tree:1` returns exactly 1 object.
4. `want <blob-oid>` alone works for a non-tip blob.
5. `want <tree>` + `filter blob:none` returns all subtrees.
6. Multiple `fetch` commands on one session work.
7. `SendEnv=GIT_PROTOCOL` still works through a ControlMaster mux client (second invocation).

Also do the same against a local repo with plain `git-upload-pack` (see §9) — that is the test target and must behave the same. If a GitHub probe fails, **stop and report to the user** rather than designing around it silently. If SSH auth to GitHub isn't available in the environment, note it and proceed using the local transport only.

---

## 4. Package layout

```
cmd/explorerepo/main.go          flag parsing, mode dispatch (-c / argv / stdin), exit codes
internal/repospec/               parse --repo into {SSHUser, Host, Path, CacheKey}
internal/pktline/                Reader (ReadPkt -> (kind, payload)), Writer (Data, Flush, Delim)
internal/transport/              Transport interface; ssh.go, local.go, counting.go (test metrics)
internal/protocol/               v2 client: Handshake/caps, LsRefs, Fetch, ObjectInfo, sideband demux
internal/object/                 OID type, Type enum, Hash(type,data), ParseTree, ParseCommit, ParseTag
internal/pack/                   streaming pack parser + OFS/REF delta resolution -> []RawObject
internal/store/                  disk cache: loose objects + refs cache with TTL; goroutine-safe
internal/repo/                   Repo: ResolveRev, Commit/root tree, Tree(oid), Blob(oid), prefetch APIs, byte budget
internal/vfs/                    path layer over Repo: Stat, ReadDir, ReadFile, Lstat, symlink resolution
internal/shell/                  mvdan interp wiring: exec/open/stat/readdir handlers, limits
internal/builtins/               one file per command + registry + shared flag helper
docs/PROBES.md, README.md
scripts/probe.sh
```

Keep packages small with clear interfaces; each gets `_test.go`.

---

## 5. Core layer details

### 5.1 repospec
Accept: `owner/repo` (→ `git@github.com:owner/repo.git`), `github.com/owner/repo`, `gitlab.com/group/sub/repo`, `git@host:path(.git)`, `ssh://user@host[:port]/path`, and `file:///abs/path` or an existing local path (→ local transport; useful for tests and local bare repos). Strip trailing `.git`/`/`, re-add `.git` for SSH. Shell-quote the path in the remote command (single quotes; reject paths containing `'` or control chars).

### 5.2 transport
```go
type Transport interface { Open(ctx) (Conn, error) }   // Conn: io.Reader + io.Writer + CloseWrite + Close + Wait error
```
- `SSHTransport`: `exec.CommandContext("ssh", "-o","SendEnv=GIT_PROTOCOL", "-o","BatchMode=yes", "-o","ControlMaster=auto", "-o","ControlPath=<dir>/%C", "-o","ControlPersist=300", ["-p",port], user@host, "git-upload-pack '<path>'")`, env `GIT_PROTOCOL=version=2`. `<dir>` = `/tmp/explorerepo-<uid>` (create 0700) — macOS unix-socket paths are capped at 104 bytes, so do **not** put it under `$TMPDIR` or the cache dir. Flag `--no-mux` disables ControlMaster. Capture ssh stderr; on failure surface it (auth errors, "Repository not found").
- `LocalTransport`: `exec git-upload-pack <path>` with `GIT_PROTOCOL=version=2`.
- `CountingTransport` wrapper: counts sessions, requests, bytes read — used by tests to assert batching.

### 5.3 protocol
- `Client{conn, caps}`; `Dial(ctx, transport)` reads caps. Parse `fetch=` feature list and other keys into `Caps`. Error clearly if not v2, if `fetch` lacks `filter` (say: "server does not support filtered fetch; explorerepo cannot work without cloning"), or `object-format` ≠ sha1 (sha256 unsupported in v1; keep OID type length-aware so it can be added).
- `LsRefs(ctx, prefixes []string) ([]Ref, error)`; `Ref{Name, OID, Peeled, SymrefTarget}`.
- `Fetch(ctx, FetchReq{Wants []OID, Filter string, Deepen int}, sink func(RawObject) error) error` — streams sideband channel 1 into the pack parser (use an `io.Pipe`, don't buffer whole pack in memory for big subtree fetches); channel 3 → error.
- `ObjectInfo(ctx, oids) (map[OID]int64, error)` only if capability present.
- One `Client` = one upload-pack session. Requests are serialized by a mutex. If the session breaks (EOF / write error), the Repo transparently redials once.
- Batch wants: cap at 1000 wants per fetch request; split larger sets.

### 5.4 pack
Streaming parser over `io.Reader` (wrap in a counting `bufio.Reader` so we know each entry's offset for OFS_DELTA). For each entry: read header, then inflate exactly one zlib stream (use `zlib.NewReader` on the bufio reader; Go's flate reader reads ahead — wrap the bufio.Reader so it implements `io.ByteReader`, which makes flate not over-read). Keep resolved objects for the duration of the pack in a map offset→(type,data) and OID→offset so deltas resolve; pending deltas whose base hasn't appeared yet (REF_DELTA to a later object) are resolved at the end. REF_DELTA bases may also come from the store. Verify trailer checksum. Emit each resolved object via sink. Reject absurd sizes (> budget) before allocating.

### 5.5 store (disk cache)
- Root: `$EXPLOREREPO_CACHE` or `os.UserCacheDir()/explorerepo`.
- `objects/ab/cdef…` in git loose format (zlib of `"<type> <len>\0"+data`) so `GIT_OBJECT_DIRECTORY=<root>/objects git cat-file -p <oid>` works for debugging. Shared across all repos (content-addressed, verified → safe). Write via temp file + rename.
- In-memory LRU in front (trees especially; cap ~64 MiB).
- `refs/<cachekey>.json`: `{fetched_at, refs:[…], head_target}`; TTL default 10 min (`--refresh` forces). Consistent snapshot across an agent's sequence of calls matters more than freshness.
- `--no-cache` → memory-only store.

### 5.6 repo
```go
type Repo struct { spec; store; client (lazy); budget }
ResolveRev(ctx, rev) (commit OID)       // "" or HEAD → HEAD; full 40-hex → as-is (no network); else refs/heads/X, refs/tags/X, then exact X ("refs/…"); abbreviated hex → error "use a full OID or a ref name"
RootTree(ctx, commit) (tree OID)        // fetch commit with deepen 1 + filter tree:1 if not cached; parse commit; root tree arrives in the same pack
Tree(ctx, oid) ([]Entry, error)         // cache, else fetch want oid + filter tree:1
Trees(ctx, oids)                         // batch: one fetch, many wants, filter tree:1
Blob(ctx, oid) ([]byte, error)          // cache, else fetch want oid
Blobs(ctx, oids)                         // batch, chunked
PrefetchSubtrees(ctx, treeOID)          // one fetch: want tree, filter blob:none → all trees below
Sizes(ctx, oids) map[OID]int64          // object-info if supported; else from cache only
```
- Every network op checks/charges a **fetch budget** (`--max-fetch`, default 256 MiB of pack bytes per invocation). Exceeding it aborts the fetch with `explorerepo: fetch budget exceeded (… MiB); narrow the path or raise --max-fetch`.
- Debug: `EXPLOREREPO_DEBUG=1` logs each request (command, #wants, filter, bytes, ms) to stderr. Invaluable for verifying batching.
- Expose `Repo.Info()` (repo spec, rev, commit OID) for shell env vars.

### 5.7 vfs
Paths are absolute virtual paths; root `/` = root tree of the selected commit.
- `Lstat`, `Stat` (follows symlinks), `ReadDir` (sorted by name like git), `ReadFile`, `Readlink`.
- Resolution walks components via `Repo.Tree`; cache resolved `path → Entry` in memory.
- Symlinks: resolve relative targets within the repo, max 40 hops; targets escaping `/` or absolute → `ENOENT`-style error "symlink points outside repository".
- Gitlinks: appear as directories that are empty; `cat`/`cd` into them → error "submodule (commit <oid>), not fetched".
- Hostile tree entries (name empty, contains `/` or NUL, `.` or `..`): hide them and warn once on stderr.
- Errors must map to familiar messages: `ls: foo: No such file or directory`, `cat: src: Is a directory`.
- `fs.FileInfo` implementation: Name, Mode (dir / regular / exec bit / symlink), Size (known size or 0 + flag "unknown"), ModTime = commit committer time.

---

## 6. Shell layer

### 6.1 Wiring (`internal/shell`)
Use `mvdan.cc/sh/v3/syntax` to parse and `interp` to run. **Verify exact API names with `go doc mvdan.cc/sh/v3/interp`** for the version you pin; at the time of writing the relevant options are `interp.StdIO`, `interp.Dir`, `interp.Env`, `interp.ExecHandlers`, `interp.OpenHandler`, `interp.ReadDirHandler2`, `interp.StatHandler`, and `interp.HandlerCtx(ctx)` inside handlers.

- `Dir("/")` — the virtual root is `/` (the real `/` exists, so any validation in `interp.Dir` passes). Every handler maps the absolute path to the vfs. **Nothing may fall through to the real filesystem or real exec.**
- `ExecHandlers`: dispatch `args[0]` to the builtin registry; unknown → stderr `explorerepo: <name>: command not available (run 'help')`, exit 127. Never call `interp.DefaultExecHandler`.
- `OpenHandler`: read-only opens resolve through vfs; `/dev/null` read/write allowed; `/dev/stdout`, `/dev/stderr` allowed; any other write/create/append → error `read-only file system` (status 1).
- `ReadDirHandler2` + `StatHandler`: through vfs, so globs (`src/*.go`), `cd`, `test -f/-d/-e`, `[ -d x ]` work.
- Env: `PWD=/`, `HOME=/`, `PATH=` (empty), plus `REPO=<spec>`, `REV=<rev as given>`, `COMMIT=<oid>`.
- Unsupported shell features to document: process substitution `<(…)`, background jobs `&`, `exec`. If the interp errors on them, make the message clear.
- Stop on context cancellation: global `--timeout` (default 60s).

### 6.2 Output limits (agent safety)
- Wrap the top-level stdout in a limiter: `--max-output` (default 200 KiB). When hit, stop writing, append `\n[explorerepo: output truncated at 200 KiB; use head/grep to narrow]\n` to **stderr**, cancel the run, exit 0 status of the script is preserved if possible (otherwise exit 0 with the notice — truncation is not an error).
- Builtins must handle downstream closure: when writing to a pipe whose reader (e.g. `head`) has exited, stop quietly (treat `io.ErrClosedPipe`/EPIPE as normal termination, exit 141 like SIGPIPE only internally, no stderr noise).

### 6.3 Builtin framework (`internal/builtins`)
```go
type Cmd func(ctx context.Context, env *Env, args []string) int   // returns exit status
type Env struct { Stdin io.Reader; Stdout, Stderr io.Writer; Dir string; FS *vfs.FS; Repo *repo.Repo; Run func(ctx, args []string) int /* for xargs */ }
```
- Shared small flag parser supporting POSIX-style clustered short flags (`-rni`), `-n 5` / `-n5` / `-5` (head/tail), `--long=value`, `--`. Unknown flag → `<cmd>: unsupported option '-X' (supported: …)`, exit 2.
- Path args resolved relative to `env.Dir` with `path.Clean`.
- Binary detection: NUL in first 8000 bytes (git's rule).

### 6.4 Builtins — v1 set and required flags

File viewing
- `cat [-n] [file…]` (no files → stdin)
- `head [-n N|-N|-c N] [file…]` (multiple files → `==> f <==` headers like GNU)
- `tail [-n N|-n +N|-c N] [file…]`
- `sed -n 'A,Bp'` / `sed -n 'Ap'` / `sed -n 'A,$p'` only (agents use this for line ranges); anything else → unsupported error suggesting `head`/`tail`.
- `wc [-l|-w|-c] [file…]` (with totals line for multiple files)
- `nl [file]` (basic), `stat [file…]` (type, mode, size-or-unknown, blob OID)

Listing / searching
- `ls [-a -l -1 -R -d -F] [path…]` — `-l` shows `mode  size  name` (size `?` when unknown; use object-info batch if available). Columns: emit one-per-line when stdout isn't a TTY (it never is for agents — always one per line).
- `tree [-L N] [-d] [path]` — like `tree`, ASCII connectors, summary line.
- `find [path…] [-name P] [-iname P] [-path P] [-type f|d|l] [-maxdepth N] [-mindepth N] [!|-not expr] [-o] [-print]` — implement a tiny expression evaluator (AND by juxtaposition, `!`, `-o`, parentheses optional/stretch). Output paths prefixed like real find (`./src/a.go` when started at `.`).
- `grep [-r|-R] [-n] [-i] [-v] [-w] [-l] [-L] [-c] [-o] [-h|-H] [-F] [-E] [-m N] [-A N] [-B N] [-C N] [-I] [-q] [--include=G] [--exclude=G] [--exclude-dir=G] [-e PAT]… PATTERN [path…]` — Go `regexp` (RE2). Translate basic-regex escapes `\|`, `\(`, `\)`, `\+`, `\?` to ERE when `-E` is not given; `-F` → `regexp.QuoteMeta`. Binary files: `Binary file X matches` (or skip with `-I`). No path + `-r` → `.`. No path, no `-r` → stdin.
- `rg PATTERN [path…] [-i] [-l] [-c] [-w] [-F] [-g GLOB]… [-n default on] [--hidden]` — thin adapter onto the grep engine (recursive, line numbers, `path:line:text` format). Agents reach for rg first.

Text utilities (stdin/file)
- `sort [-r -n -u -k N -t C]`, `uniq [-c -d]`, `cut [-d C] [-f LIST] [-c LIST]`, `tr [-d] SET1 [SET2]` (simple ranges), `xargs [-n N] [-I R] [-0]` (re-dispatches through `env.Run`, i.e. only builtins), `basename`, `dirname`, `tee` (stdout only; file args → read-only error).
- Shell builtins (`echo`, `printf`, `test`, `[`, `cd`, `pwd`, `read`, `true`, `false`, loops, …) come from the interp.

Repo-specific
- `refs [PREFIX]` — ls-refs (`refs/heads/`, `refs/tags/` by default), prints `<oid> <name>`; marks HEAD's target.
- `log [-n N] [--oneline]` — fetch `want <commit>` + `deepen N` + `filter tree:0`; print commits (no path filtering — say so in help).
- `git` — minimal shim, because agents will try it: `git log [-n N] [--oneline]`, `git show REV:PATH` (resolve REV, read path), `git ls-tree [-r] [--name-only] REV [PATH]`, `git rev-parse HEAD`. Anything else → "only these git subcommands are available".
- `pull [-f] PATH [DEST]` — the only host-FS writer (see §7).
- `help [cmd]` — concise manual (see §8).

### 6.5 Batching rules (the whole point — must be implemented and tested)
- `ReadDir` of a dir: one `Tree` fetch at most (children's trees are not fetched until needed).
- Recursive walk (`find`, `ls -R`, `tree`, `grep -r`, `rg`, `git ls-tree -r`, `pull`) rooted at tree T: if T's full subtree isn't known to be cached, call `PrefetchSubtrees(T)` **once** (blob:none), then walk purely from cache. Exception: `find -maxdepth 1|2` / `tree -L 1|2` walk lazily level by level, batching each level's missing trees into one `Trees` request.
- Content scans (`grep -r`, `rg`, `pull`, `xargs cat` where possible): walk → apply include/exclude/type filters → collect blob OIDs not in cache → `Blobs` in chunks of ≤1000 → then scan. Cap with `--max-files` (default 20 000 files per scan): exceeding it errors *before* fetching with a hint to narrow path/`--include`.
- `cat a b c` / `head` with multiple file args: resolve all paths first, then one `Blobs` call.
- Target: `grep -rn foo some/dir` on a cold cache = ls-refs (maybe cached) + commit/root + ≤ depth tree fetches to reach `some/dir` + 1 subtree fetch + ⌈N/1000⌉ blob fetches.

---

## 7. `pull`

`pull [-f] PATH [DEST]`: file → write file; dir → materialize subtree. DEST default `./<basename(PATH)>` relative to the **real** process cwd (the `-c` virtual cwd is irrelevant here; document that). Refuse if DEST exists unless `-f`. Batch-fetch exactly as §6.5. Safety: validate every path component (reject `..`, `.git` case-insensitively, names with `/`, `\`, NUL, absolute); create symlinks only if the target resolves inside DEST, otherwise write a regular file containing the target and warn; skip gitlinks with a warning; set exec bit for 100755; detect case-folding collisions on case-insensitive filesystems (macOS default) and error. Print `pulled N files (X MiB) to DEST`.

---

## 8. CLI surface (`cmd/explorerepo`)

```
explorerepo --repo SPEC [--rev REV] [options] -c 'SCRIPT'
explorerepo --repo SPEC [--rev REV] [options] CMD [ARGS…]     # single builtin, no shell parsing
explorerepo --repo SPEC [--rev REV] [options] < script.sh
explorerepo help [CMD]

options: --rev (alias --hash)  --refresh  --no-cache  --no-mux
         --max-output BYTES(200K)  --max-fetch BYTES(256M)  --max-files N(20000)  --timeout DUR(60s)
env:     EXPLOREREPO_REPO (default for --repo), EXPLOREREPO_CACHE, EXPLOREREPO_DEBUG=1
```
- Byte flags accept `K/M/G` suffixes.
- Exit status = script's exit status; tool/setup errors (auth, repo not found, bad rev) → stderr `explorerepo: …`, exit 128 (git convention).
- `help` text is the agent's manual: ≤ ~60 lines, list commands with supported flags, the cost model in two lines ("directory listings are cheap; recursive grep fetches every file under the path — narrow with a path or --include"), and examples. Keep it exact: anything listed must work.

---

## 9. Testing

All tests must run offline with `go test ./...` using `LocalTransport`.

- **Fixture builder** (`internal/testutil`): create a temp non-bare repo with the `git` CLI, commit a known layout: nested dirs (depth ≥ 4), a file at each level, unicode and spaces in names, an executable, a symlink inside the repo, a symlink escaping the repo, a binary file, a 3 MiB file, an empty file, a submodule gitlink (`git update-index --add --cacheinfo 160000,<oid>,sub`), two commits so deltas exist (modify a big text file), a lightweight and an annotated tag, a second branch. Then `git clone --bare` it, `git repack -adf` (forces deltas in server packs), and set `uploadpack.allowFilter=true`, `uploadpack.allowAnySHA1InWant=true` on the bare repo.
- **Unit**: pktline round-trip & malformed lengths; pack parser on packs produced by `git pack-objects --stdout` (with `--delta-base-offset` and without → REF_DELTA) checked against `git cat-file`; delta applier edge cases (copy size 0 = 0x10000); tree/commit parsers; repospec table; flag parser; each builtin against expected output (compare to real GNU/BSD semantics where cheap, but hardcode expectations — don't depend on host coreutils).
- **Integration**: run scripts through the full stack, e.g. `ls -la`, `cat sub/dir/file | wc -l`, `find . -name '*.txt' | sort`, `grep -rn needle .`, `cd a/b && head -3 c.txt`, `xargs`, globbing, redirect errors, unknown-command errors, symlink behavior, gitlink errors, `--rev` with branch/tag/annotated tag/full OID/abbrev (error), `pull` into temp dir then compare against `git archive` output.
- **Batching assertions** with `CountingTransport` + cold cache: e.g. `cat d1/d2/d3/f` = exactly 1 ls-refs + 1 commit fetch + 3 tree fetches + 1 blob fetch; same command again with warm cache and fresh ref cache = 0 fetches and 0 sessions; `grep -r x d1` = bounded number of fetches independent of file count.
- **Hostile input**: construct a tree with `..` / `.git` entries via `git mktree` + `git commit-tree` (git allows creating them with plumbing), ensure vfs hides them and `pull` refuses.
- **Live smoke test** (skipped unless `EXPLOREREPO_LIVE=1`): `octocat/Hello-World` `cat README`, and `golang/go` `ls src` + `grep -rn 'func Println' src/fmt`.

---

## 10. Milestones (implement in order; each ends green with tests)

1. **M0** Probes (§3) → `docs/PROBES.md`. `go mod init github.com/<user>/explorerepo` (ask/keep generic: `explorerepo`), Makefile/`justfile` optional.
2. **M1** pktline, transport (ssh/local/counting), protocol (caps, ls-refs, fetch with sideband), pack parser + deltas, object parsing, verification. CLI debug commands hidden behind `explorerepo debug lsrefs|fetch OID [--filter …]`.
3. **M2** store (disk + LRU + refs TTL), repo (ResolveRev/RootTree/Tree/Blob/batch/prefetch/budget), vfs.
4. **M3** shell wiring + limits + argv/stdin modes + builtins: `ls cat head tail wc stat pwd/cd(sh) help`.
5. **M4** recursive & search: `find tree grep rg ls -R`, batching rules, `--max-files`, text utilities (`sort uniq cut tr xargs basename dirname nl sed-range tee`).
6. **M5** `pull`, `refs`, `log`, `git` shim, README (install, usage, agent system-prompt snippet), live smoke test.

## 11. Out of scope for v1 (note in README)
SHA-256 repos; protocol v0 servers; HTTP(S) transport; Git LFS content (pointer files are shown as-is — mention in help); path-limited `log`; background daemon; FUSE/NFS mount; writing/pushing anything.

## 12. Notes for the implementer
- When an exact behavior of git/upload-pack is uncertain, check it empirically against local `git-upload-pack` (`GIT_TRACE_PACKET=1` helps) rather than guessing, and record surprises in `docs/PROBES.md`.
- Prefer clear, familiar error messages over cleverness — the consumer is an LLM that pattern-matches on coreutils behavior.
- Don't commit until asked; keep commits per milestone if asked.

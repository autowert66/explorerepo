# Phase 0 probes

Results of probing `git-upload-pack` protocol v2 before building on it. Reproduce with
`scripts/probe.sh ssh octocat/Hello-World.git` (GitHub) and
`scripts/probe.sh local /path/to/bare.git`; the Go-level equivalents are
`EXPLOREREPO_LIVE=1 go test ./internal/protocol -run Live -v` and
`explorerepo --repo SPEC debug caps|lsrefs|fetch`.

Probed: GitHub (`agent=git/github-f6e509caf74d-Linux`, 2026-11-06) and local git 2.56.0.

## Summary of surprises (read this first)

| # | Plan assumption | Reality | What the implementation does |
|---|---|---|---|
| A | `GIT_PROTOCOL=version=2` + `SendEnv` gives v2 over ssh | **True, unless the user's ssh config has a `ControlMaster`** that was started without `SendEnv=GIT_PROTOCOL`. A mux client silently loses the variable and GitHub answers with a **v0** ref advertisement. Real `git` is affected too (`git -c protocol.version=2 ls-remote git@github.com:...` returned v0 on the probing machine). | The ssh transport always passes its own `ControlPath=/tmp/explorerepo-<uid>/%C` (+ `ControlMaster=auto`, `ControlPersist=300`), so it never reuses a foreign master. `--no-mux` uses `ControlPath=none`. A v0 reply produces a clear error mentioning ControlMaster. |
| B | `deepen 1` + `filter tree:1` on a commit returns commit + root tree in one request | **GitHub rejects it**: `ERR tree filter allows max depth 0, but got 1`. Only `tree:0` (and `blob:*`) are allowed. Local git accepts `tree:1`. | Portable path: commit via `deepen 1` + `tree:0` (commit only), root tree via a second `tree:0` request. One extra round trip on a cold start; when the first walk is recursive, the root tree arrives inside the `blob:none` subtree prefetch instead. |
| C | `want <tree>` + `filter tree:1` returns just that tree | Wrong on local git 2.56 (and consistent with the depth rule): an explicitly wanted tree counts as depth -1, so `tree:1` also returns its children (6 trees + 7 blobs for the fixture root). **`tree:0` returns exactly that one tree** on both GitHub and local. | Single-tree fetches use `tree:0`. |
| D | `object-info` may be advertised | **Not advertised by GitHub** (and not by default locally). | `ls -l` prints `?` for sizes of blobs that are not cached yet; sizes appear after the content has been read once. `object-info` is used automatically if a server advertises it. |

## Probes

1. **Capability advertisement.** GitHub v2: `version 2`, `agent=...`, `ls-refs=unborn`,
   `fetch=shallow wait-for-done filter`, `server-option`, `object-format=sha1`. `filter` is present;
   `object-info` is absent (D). The `fetch` command does *not* advertise `ref-in-want`, `sideband-all`
   or `packfile-uris`, none of which are needed.
2. **`ls-refs` with `ref-prefix HEAD` + `symrefs`.** Returns `<oid> HEAD symref-target:refs/heads/master` (GitHub)
   / `refs/heads/main` (fixture). `peel` yields `peeled:<oid>` for annotated tags. Prefix filtering keeps the
   answer small even for repos with hundreds of thousands of `refs/pull/*`.
3. **Single-object shapes** (GitHub, `octocat/Hello-World`):
   - `want <commit>` `deepen 1` `filter tree:0` -> 1 object (the commit). `filter tree:1` -> `ERR` (B).
   - `want <tree>` `filter tree:0` -> 1 object. (C)
4. **`want <oid>` for a non-tip blob/tree.** Works on GitHub with no extra configuration (every tree and blob
   fetched in the tests was a non-tip object). Locally this needs `uploadpack.allowAnySHA1InWant=true` (the test
   fixture sets it; so do the `allowFilter` setting).
5. **`want <tree>` + `filter blob:none`.** Returns every tree below, no blobs, in one request. Measured on
   `golang/go`: `src/` -> 1445 trees, 420 535 pack bytes, ~0.5 s. (`blob:limit=1k` on the same tree: 6042 objects,
   1.7 MB.) This is the primitive behind `find`/`grep -r`/`tree`/`pull`.
6. **Several `fetch` commands on one session.** Works on GitHub and locally (the probe script sees two `packfile`
   sections; the Go client reuses one session for ls-refs plus any number of fetches).
7. **Mux behaviour.** With a private `ControlPath`, the second invocation is a mux client and still speaks v2
   (A). Cost on the probing machine: first connection ~1.4 s (handshake + auth), subsequent ones ~0.3 s.
8. **Local transport.** `git upload-pack` with `GIT_PROTOCOL=version=2` behaves the same as GitHub for everything
   above except B and C.

## Other observations

- With a piped (non-interactive) request stream, ssh must keep stdin open until the response arrives; a request that
  is immediately followed by EOF can come back empty from GitHub. The real client never closes stdin early;
  `probe.sh` sleeps 2 s for this reason.
- If the macOS ssh agent cannot sign (e.g. Secretive/Touch ID dismissed) the first connection fails with
  `sign_and_send_pubkey: signing failed ... communication with agent failed`; explorerepo surfaces ssh's stderr verbatim.
- ssh `ControlPersist` forks a master that inherits the client's stderr pipe; the transport therefore owns that pipe
  (rather than letting `os/exec` copy it) so shutdown never waits for the master.
- Typical cold costs against GitHub: `ls src` on `golang/go` ~1.3 s (connect + ls-refs + commit + root + `src`),
  `grep -rn 'func Println' src/fmt` ~0.9 s with refs cached, `grep -rn sched_tick kernel/sched | head -3` on
  `torvalds/linux@v6.11` ~1.8 s including a tag-peeling `ls-refs`.

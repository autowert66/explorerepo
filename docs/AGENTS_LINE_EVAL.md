# AGENTS.md line evaluation

How the line printed by `explorerepo help agents` (`builtins.AgentsLine`) was chosen.

Setup (2026-11-06): opencode 2.2.5, `opencode run --standalone --auto` with DeepSeek V4.1 Flash, run in an
empty directory with the user's real global config, where only the explorerepo line of `AGENTS.md` was swapped
(`XDG_CONFIG_HOME` pointing at a copy whose entries are symlinks). Seven prompts: bat paging (multi-file
exploration), ripgrep mmap, zod's email regex and httpx's defaults (repo not named), bubbletea FPS and fzf
`--hscroll-off` (repo named), and a control prompt where the tool should not be used. The answers were correct for
every variant that used the tool, so variants differ in tool choice and calls.

Mean shell/tool calls per task (n = 2 for none/v0, 6 for v2/v7):

| task      | no line | old line (v0) | v2  | v7 (current) |
|-----------|---------|---------------|-----|--------------|
| bat       | 12.5    | 8.5           | 7.0 | 5.5          |
| ripgrep   |         |               | 6.5 | 4.7          |
| zod       | 4.5     | 7.5           | 6.0 | 5.0          |
| httpx     | 7.5     | 5.5           | 2.5 | 2.3          |
| bubbletea | 10.5    | 2.0           | 1.8 | 1.5          |
| control   | 0.5     | 0             | 0   | 0            |

Findings:

- The old line said what the tool is, not when to use it. With the repo unnamed (zod, httpx), it was skipped in
  half the runs for webfetch of raw GitHub URLs or websearch. Naming the alternatives to avoid (clone, raw
  GitHub URLs, `gh api`, memory) fixed that.
- Every call re-sends the whole context (~12k tokens), so tokens track the number of calls. A two-step recipe
  (locate in one script, then read every range in one script) cut calls more than "batch commands" alone.
  Steering toward `cat -n` made the model read whole files, which cost more.
- "Use `explorerepo`" was read as an MCP tool by the code-mode `execute` tool (`tools.explorerepo({...})`) in
  several runs, once ending in `git clone`; "run ... in your shell (it is a CLI, not an MCP tool)" removed it.
- Unquoted labels such as `echo === out.rs (pager) ===` are a shell syntax error that fails the whole script;
  the examples now quote their labels. Models also reach for `awk`, which does not exist, so the line says so.
- A shorter one-sentence line (v3) needed more calls than the longer recipe.

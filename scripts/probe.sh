#!/usr/bin/env bash
# Phase 0 probes: hand-crafted protocol v2 requests against git-upload-pack.
#
#   scripts/probe.sh ssh [owner/repo.git]     # GitHub over ssh (default octocat/Hello-World.git)
#   scripts/probe.sh local /path/to/bare.git  # local git upload-pack
#
# Requests are piped in, so stdin is held open for 2s after the last one: if it hits EOF at once
# the ssh side may stop before answering (a real client keeps stdin open).
#
# Note the private ControlPath: a ControlMaster from the user's ~/.ssh/config that
# was started without SendEnv=GIT_PROTOCOL silently drops the variable and the server
# answers with a v0 ref advertisement.
set -u
MODE=${1:-ssh}
TARGET=${2:-octocat/Hello-World.git}
MUX=/tmp/explorerepo-probe-$(id -u)
mkdir -p -m 700 "$MUX"

pkt()  { local s="$1"; printf '%04x%s' $(( ${#s} + 4 )) "$s"; }
FLUSH='0000'; DELIM='0001'

run() {
  if [ "$MODE" = ssh ]; then
    GIT_PROTOCOL=version=2 ssh -o SendEnv=GIT_PROTOCOL -o BatchMode=yes \
      -o ControlMaster=auto -o ControlPath="$MUX/%C" -o ControlPersist=60 \
      git@github.com "git-upload-pack '$TARGET'"
  else
    GIT_PROTOCOL=version=2 git upload-pack "$TARGET"
  fi
}
cmd() { # cmd NAME ARG... -> one v2 request (flush-terminated), then a final flush to end the session
  local name=$1; shift
  pkt "command=$name"$'\n'; pkt $'object-format=sha1\n'; printf '%s' $DELIM
  for a in "$@"; do pkt "$a"$'\n'; done
  printf '%s' $FLUSH
}

echo "== 1. capability advertisement"
printf '%s' $FLUSH | run 2>&1 | strings | head -12

echo "== 2. ls-refs HEAD + symrefs (expect symref-target:)"
{ cmd ls-refs peel symrefs 'ref-prefix HEAD'; printf '%s' $FLUSH; sleep 2; } | run 2>&1 | strings | grep -a -A3 ' HEAD'  | head -3

HEAD_OID=$({ cmd ls-refs 'ref-prefix HEAD'; printf '%s' $FLUSH; sleep 2; } | run 2>&1 | grep -aoE '[0-9a-f]{40} HEAD' | head -1 | cut -c1-40)
echo "HEAD = $HEAD_OID"

echo "== 3. want <commit> deepen 1 filter tree:0 (expect one small packfile: the commit)"
{ cmd fetch ofs-delta no-progress 'deepen 1' 'filter tree:0' "want $HEAD_OID" done; printf '%s' $FLUSH; sleep 2; } \
  | run 2>/dev/null | wc -c | xargs echo "response bytes:"

echo "== 3b. same with filter tree:1 (GitHub rejects: 'tree filter allows max depth 0')"
{ cmd fetch ofs-delta no-progress 'deepen 1' 'filter tree:1' "want $HEAD_OID" done; printf '%s' $FLUSH; sleep 2; } \
  | run 2>&1 | strings | grep -a -m1 'max depth' || echo "(accepted)"

echo "== 6. two fetches on one session (expect two 'packfile' sections)"
{ cmd fetch ofs-delta no-progress 'deepen 1' 'filter tree:0' "want $HEAD_OID" done
  cmd fetch ofs-delta no-progress 'deepen 1' 'filter tree:0' "want $HEAD_OID" done
  printf '%s' $FLUSH; sleep 2; } | run 2>/dev/null | grep -ao 'packfile' | wc -l | xargs echo "packfile sections:"

echo "== 7. second invocation through the mux still speaks v2"
printf '%s' $FLUSH | run 2>&1 | strings | head -1
[ "$MODE" = ssh ] && ssh -O exit -o ControlPath="$MUX/%C" git@github.com 2>/dev/null
exit 0

#!/bin/bash
# Fresh-container setup: credential, web search grant, optional explorerepo; then one run.
set -e
opencode auth import --standalone /run/go.json >/dev/null 2>&1
sqlite3 ~/.local/share/opencode/opencode.db "insert or replace into kv(key,value,time_created,time_updated) values('websearch:provider',json_quote('random'),0,0)"
mkdir -p ~/.config/opencode ~/.local/bin
if [ "$VARIANT" = "with" ]; then
  cp /opt/explorerepo/explorerepo ~/.local/bin/explorerepo
  ~/.local/bin/explorerepo help agents > ~/.config/opencode/AGENTS.md
fi
export PATH="$HOME/.local/bin:$PATH"
MODEL=${MODEL:-opencode-go/deepseek-v4.1-flash}
version=$(opencode --version)
cd ~/work
start=$(date +%s%3N)
set +e
timeout "${TIMEOUT:-600}" opencode run --standalone --auto -m "$MODEL" --format json "$PROMPT" </dev/null > /out/events.jsonl 2> /out/stderr.txt
code=$?
end=$(date +%s%3N)
echo "{\"model\":\"$MODEL\",\"opencode\":\"$version\",\"exit\":$code,\"start_ms\":$start,\"end_ms\":$end,\"wall_ms\":$((end-start))}" > /out/meta.json

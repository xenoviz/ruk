#!/usr/bin/env bash
# Compare N ready-to-run workspaces created with plain `git worktree add` plus
# `install --frozen-lockfile` against Ruk's first acquire and pool reuse.
#
# Usage: RUK=/path/to/ruk scripts/benchmark/workspaces.sh <work-dir> <owner/repo>...
#
# Each repository is shallow-cloned into <work-dir>/src. The package manager's
# global cache is warmed once per repository before timing, and HOME is
# isolated under <work-dir>. Results are appended to <work-dir>/results.jsonl
# and Ruk's counters are saved as <work-dir>/stats-<repo>.json. pnpm and Bun
# must be on PATH; supported versions select Ruk's shared dependency mode.
set -uo pipefail
if [ $# -lt 2 ]; then
  echo "usage: RUK=/path/to/ruk $0 <work-dir> <owner/repo>..." >&2
  exit 2
fi
RUK="${RUK:?set RUK to the ruk binary}"
N="${N:-3}"
mkdir -p "$1"
B="$(cd "$1" && pwd)"
shift
export HOME="$B/home"
export GIT_CONFIG_GLOBAL="$B/gitconfig"
export CI=1 # suppress interactive installer prompts on both sides
mkdir -p "$HOME" "$B/src" "$B/wt"
git config --file "$GIT_CONFIG_GLOBAL" user.name bench
git config --file "$GIT_CONFIG_GLOBAL" user.email bench@example.com
git config --file "$GIT_CONFIG_GLOBAL" commit.gpgsign false
RESULTS="$B/results.jsonl"
LOG="$B/run.log"

now() { date +%s.%N; }
used_kb() { sync; df --output=used -k "$B" | tail -1 | tr -d ' '; }
seconds() { printf '%.3f' "$(echo "$2 - $1" | bc)"; }
record() { # repo scenario index seconds [extra-json]
  printf '{"repo":"%s","scenario":"%s","index":%s,"seconds":%s%s}\n' "$1" "$2" "$3" "$4" "${5:+,$5}" >>"$RESULTS"
}
timed() { # repo scenario index -- command...
  local repo=$1 scenario=$2 index=$3 start status elapsed
  shift 4
  start=$(now)
  "$@" >>"$LOG" 2>&1
  status=$?
  elapsed=$(seconds "$start" "$(now)")
  record "$repo" "$scenario" "$index" "$elapsed" "\"exit\":$status"
  echo "$(date +%T) $repo $scenario $index exit=$status ${elapsed}s" | tee -a "$LOG"
  return $status
}
# Confirm dependencies are installed: the root manifest's first dependency
# must have a readable package.json in the workspace's node_modules.
verify() { # dir
  (cd "$1" && node -e '
    const fs = require("fs");
    const p = require("./package.json");
    const dep = Object.keys({ ...p.devDependencies, ...p.dependencies })[0];
    const manifest = JSON.parse(fs.readFileSync(fs.realpathSync("node_modules/" + dep + "/package.json"), "utf8"));
    console.log("resolved", dep, manifest.version);')
}
field() { node -p "require('$1').$2"; }

for spec in "$@"; do
  repo=$(basename "$spec")
  src="$B/src/$repo"
  [ -d "$src" ] || git clone -q --depth 1 "https://github.com/$spec" "$src" || continue
  cd "$src" || continue
  echo "=== $repo ===" | tee -a "$LOG"
  pm=pnpm
  [ -f bun.lock ] && pm=bun

  # 1. Warm the package manager's global cache once, like a developer machine.
  timed "$repo" warmup 0 -- $pm install --frozen-lockfile

  # 2. Baseline: git worktree add plus the same install Ruk runs.
  before=$(used_kb)
  for i in $(seq 1 "$N"); do
    wt="$B/wt/$repo-base-$i"
    timed "$repo" baseline "$i" -- bash -c "git worktree add -q -b bench-base-$i '$wt' HEAD && cd '$wt' && $pm install --frozen-lockfile"
    verify "$wt" >>"$LOG" 2>&1 || echo "VERIFY FAILED baseline $repo $i" | tee -a "$LOG"
  done
  record "$repo" baseline-disk 0 0 "\"kb\":$(($(used_kb) - before)),\"workspaces\":$N"
  for i in $(seq 1 "$N"); do
    git worktree remove --force "$B/wt/$repo-base-$i" >>"$LOG" 2>&1
    git branch -q -D "bench-base-$i" >>"$LOG" 2>&1
  done
  git clean -qfdx >>"$LOG" 2>&1

  # 3. Ruk: prepare the primary checkout, then N first acquires.
  timed "$repo" ruk-init 0 -- "$RUK" init --json
  before=$(used_kb)
  ids=()
  for i in $(seq 1 "$N"); do
    out="$B/acquire-$repo-$i.json"
    timed "$repo" ruk-acquire "$i" -- bash -c "'$RUK' acquire bench-ruk-$i --owner bench --json > '$out'"
    ids+=("$(field "$out" assignmentId)")
    verify "$(field "$out" path)" >>"$LOG" 2>&1 || echo "VERIFY FAILED ruk $repo $i" | tee -a "$LOG"
  done
  record "$repo" ruk-disk 0 0 "\"kb\":$(($(used_kb) - before)),\"workspaces\":$N"

  # 4. Ruk: release to the pool, then N acquires that reuse prepared slots.
  for id in "${ids[@]}"; do "$RUK" release "$id" --json >>"$LOG" 2>&1; done
  for i in $(seq 1 "$N"); do
    out="$B/reacquire-$repo-$i.json"
    timed "$repo" ruk-reuse "$i" -- bash -c "'$RUK' acquire bench-reuse-$i --owner bench --json > '$out'"
    node -e "if (!require('$out').reused) process.exit(1)" || echo "NOT REUSED $repo $i" | tee -a "$LOG"
    verify "$(field "$out" path)" >>"$LOG" 2>&1 || echo "VERIFY FAILED reuse $repo $i" | tee -a "$LOG"
  done
  "$RUK" stats --json >"$B/stats-$repo.json" 2>>"$LOG"
done
echo "DONE" | tee -a "$LOG"

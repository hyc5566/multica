// Only the newly launched child is terminated on a readiness timeout. Never
// replace a live bundle if that child cannot be stopped; keep rollback assets.
export const SWAP_SCRIPT = `#!/bin/bash
set -u
pid=$1 current=$2 next=$3 rollback=$4 log=$5 executable=$6 token=$7 ready=$8 result=$9
attempts=\${10:-120}
exec >>"$log" 2>&1
echo "[$(date -u +%FT%TZ)] swap $current"
outcome() { printf '%s\\n' "$1" > "$result"; }
for _ in $(seq 1 240); do kill -0 "$pid" 2>/dev/null || break; sleep 0.5; done
if kill -0 "$pid" 2>/dev/null; then echo "App still running; update skipped"; outcome quit-timeout; exit 1; fi
if ! rm -rf -- "$rollback" || ! mkdir -p -- "$(dirname "$rollback")"; then
  outcome recovery-required; open -- "$current"; exit 1
fi
if ! mv -- "$current" "$rollback"; then outcome move-failed; open -- "$current"; exit 1; fi
restore() {
  failed="$(dirname "$rollback")/failed-$(basename "$current")"
  rm -rf -- "$failed"
  if [ -e "$current" ] && ! mv -- "$current" "$failed"; then outcome recovery-required; exit 1; fi
  if ! mv -- "$rollback" "$current"; then outcome recovery-required; exit 1; fi
  outcome restored
  if ! open -- "$current"; then outcome recovery-required; fi
  exit 1
}
if ! mv -- "$next" "$current"; then echo "cannot install new bundle; restoring"; restore; fi
xattr -dr com.apple.quarantine "$current" 2>/dev/null || true
"$current/Contents/MacOS/$executable" "--zh-tw-update-token=$token" &
child=$!
for _ in $(seq 1 "$attempts"); do
  if [ -f "$ready" ] && kill -0 "$child" 2>/dev/null; then
    echo "new App ready; rollback kept at $rollback"
    outcome installed
    exit 0
  fi
  if ! kill -0 "$child" 2>/dev/null; then echo "new App exited before ready"; restore; fi
  sleep 0.5
done
echo "new App did not report ready; stopping child $child"
kill -TERM "$child" 2>/dev/null || true
for _ in $(seq 1 50); do kill -0 "$child" 2>/dev/null || break; sleep 0.1; done
if kill -0 "$child" 2>/dev/null; then
  echo "new App still running; leaving bundle and rollback for manual recovery"
  outcome recovery-required
  exit 1
fi
restore
`;

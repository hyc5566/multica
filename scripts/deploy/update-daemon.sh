#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
  echo "Usage: $0 <profile> <systemd-user-unit> <staged-binary> <expected-sha256>" >&2
  echo "Run as the daemon user on one Linux host. Updates only a running, idle daemon owned by the named unit." >&2
  exit 2
}

[[ $# == 4 ]] || usage
profile=$1
unit=$2
staged=$3
expected=${4,,}

command -v multica >/dev/null || { echo "multica CLI is required" >&2; exit 1; }
command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
command -v systemctl >/dev/null || { echo "systemctl is required" >&2; exit 1; }
[[ -f "$staged" && -x "$staged" ]] || { echo "staged binary must be an executable file: $staged" >&2; exit 1; }
[[ "$expected" =~ ^[0-9a-f]{64}$ ]] || { echo "expected SHA-256 must be 64 hex characters" >&2; exit 2; }
actual=$(sha256sum -- "$staged" | awk '{print $1}')
[[ "$actual" == "$expected" ]] || { echo "staged binary SHA-256 mismatch" >&2; exit 1; }

status=$(multica daemon status --output json)
[[ $(jq -r '.status' <<<"$status") == running ]] || { echo "daemon is not running" >&2; exit 1; }
[[ $(jq -r '.profile' <<<"$status") == "$profile" ]] || { echo "daemon profile mismatch" >&2; exit 1; }
[[ $(jq -r '.active_task_count' <<<"$status") == 0 ]] || { echo "daemon has active tasks; wait for idle" >&2; exit 1; }
pid=$(jq -r '.pid' <<<"$status")
[[ "$pid" =~ ^[1-9][0-9]*$ && -e "/proc/$pid/exe" ]] || { echo "daemon PID is unavailable" >&2; exit 1; }
[[ $(systemctl --user show "$unit" --property=ActiveState --value) == active ]] || { echo "unit is not active: $unit" >&2; exit 1; }
unit_pid=$(systemctl --user show "$unit" --property=MainPID --value)
[[ "$unit_pid" == "$pid" ]] || { echo "unit MainPID does not own the daemon PID" >&2; exit 1; }

binary=$(readlink -f -- "/proc/$pid/exe")
[[ -n "$binary" && -f "$binary" && ! "$binary" =~ \ \(deleted\)$ ]] || { echo "cannot resolve the running daemon binary" >&2; exit 1; }
[[ $(stat -c %u -- "$binary") == "$(id -u)" ]] || { echo "run as the daemon binary owner" >&2; exit 1; }
backup="$binary.pre-update.$(date +%Y%m%dT%H%M%S%z)"
[[ ! -e "$backup" ]] || { echo "backup already exists: $backup" >&2; exit 1; }

rollback() {
  echo "Update failed; restoring $backup" >&2
  systemctl --user stop "$unit" || true
  cp -p -- "$backup" "$binary.rollback.$$"
  mv -f -- "$binary.rollback.$$" "$binary"
  systemctl --user start "$unit" || true
}

cp -p -- "$binary" "$backup"
systemctl --user stop "$unit"
tmp="$binary.update.$$"
if ! install -m "$(stat -c %a -- "$binary")" -- "$staged" "$tmp" || ! mv -f -- "$tmp" "$binary"; then
  rm -f -- "$tmp"
  rollback
  exit 1
fi
if ! systemctl --user start "$unit"; then
  rollback
  exit 1
fi

for _ in {1..30}; do
  status=$(multica daemon status --output json 2>/dev/null || true)
  if [[ -n "$status" ]] && [[ $(jq -r '.status' <<<"$status" 2>/dev/null || true) == running ]]; then
    pid=$(jq -r '.pid' <<<"$status")
    if [[ "$pid" =~ ^[1-9][0-9]*$ && -e "/proc/$pid/exe" ]] && [[ $(sha256sum -- "/proc/$pid/exe" | awk '{print $1}') == "$expected" ]]; then
      [[ $(jq -r '.profile' <<<"$status") == "$profile" && $(jq -r '.active_task_count' <<<"$status") == 0 ]] || { rollback; exit 1; }
      echo "Updated $profile via $unit; daemon_id=$(jq -r '.daemon_id' <<<"$status"), binary=$binary, sha256=$expected, backup=$backup"
      exit 0
    fi
  fi
  sleep 1
done
rollback
exit 1

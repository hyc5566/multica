# Server handoff tooling

繁體中文版的主要維運程序：[Server 平順切換維運程序](../../docs/server-handoff.zh-tw.md)。

Follow that runbook before updating a Taiwan-edition Server. It covers one-time
shared-state/proxy initialization, same-schema preflight, cutover, verification,
rollback, session drain and Compose reconciliation. Keep the procedure in that
single document; this directory is the tool entry point.

- [server-handoff.py](server-handoff.py): preview by default; explicit apply starts
  a candidate, verifies readiness and switches the API routes while retaining the
  old Server.
- [test-server-handoff.py](test-server-handoff.py): isolated real-Server,
  PostgreSQL, Redis and Caddy exercise with authenticated WebSockets and rollback.
- [update-daemon.sh](update-daemon.sh): one-host Linux daemon updater; requires
  an idle daemon whose PID is owned by the named active user service, plus a
  staged executable and its reviewed SHA-256. Run separately on each host.

The tool does not migrate live sockets or permit incompatible database changes.

## Desktop update credential check

[desktop-quota-check.py](desktop-quota-check.py) is the post-update gate for
Desktop maintenance. With `--update-command`, it runs an already-authorized
installer and then automatically requests quota from the updated daemon. Without
that option it only runs the quota check. It does not restart the daemon itself.

The installed daemon must support native renewal (Taiwan edition `.15` or later).
The request uses the daemon's credential context, not the SSH process's Google
credentials. Existing valid credentials are reused; expired native credentials
are renewed by the daemon when possible. Revoked grants still require login.
Only a successful new quota result and stored snapshot pass; retained old quota,
a failed attempt, an unexpected daemon version or a timeout return exit code 1.
This is deployment tooling, not a hook inside Electron's automatic updater.

Run on the target Mac, using that machine's approved Python environment. Example
arguments (replace the runtime and version with the approved update target):

```sh
"$HOME/miniconda3/envs/hungyu/bin/python" scripts/deploy/desktop-quota-check.py \
  --profile desktop-10.1.24.90-45671 \
  --workspace 76194abc-bda7-4995-8b18-a90fe46d9dce \
  --runtime 0699adbe-b52d-4f5f-a982-46f11a7df5c4 \
  --expected-version 0.4.41-zh-tw.15
```

For a single automated update/check invocation, append `--update-command`
followed by the existing approved installer command and its arguments (this
option must be last). The installer must finish with the expected daemon ready;
its failure stops the sequence. Capture its standard error as the installation
log and this tool's standard output as the credential-free JSON result. The
installer remains responsible for host/version checks, idle-task gating,
backups and authorized rollback. A quota-check failure does not automatically
roll back the App or change credentials, CA, permissions or daemon preferences.

The freshness cutoff is set only after the installer finishes and the expected
daemon is confirmed running. Snapshots collected by the old daemon during
installation cannot pass. If a server cooldown returns an older cached result,
the check fails closed; run it again after the cooldown. Do not lower the cutoff
to make a cached result pass. An unexpected redirect is rejected without
forwarding the account credential.

Local check (no accounts, network, installed CLIs or services):

```sh
"$HOME/miniconda3/envs/hungyu/bin/python" scripts/deploy/test-desktop-quota-check.py
```

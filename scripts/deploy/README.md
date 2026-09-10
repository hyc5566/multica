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

The tool does not migrate live sockets or permit incompatible database changes.

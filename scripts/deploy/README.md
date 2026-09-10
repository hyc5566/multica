# Same-schema Server handoff

This is an operator tool for an already prepared single-host Docker deployment.
It prepares a second Server and switches both public and local API proxy routes.
It retains the old Server. It does not promise migration of live sockets or
terminal/stream sessions, zero data loss under every failure, or database HA.

## One-time prerequisites

- All Server nodes use the same authenticated Redis for request stores and
  sharded realtime fanout (`REDIS_URL`; default `REALTIME_RELAY_MODE=sharded`).
  Use a persistent volume, AOF, `noeviction`, a private network and bounded memory.
  Redis introduction is a separate operational change: finish pending model,
  usage, update and skill requests before moving from in-memory stores. Existing
  in-memory pending records are not copied to Redis.
- Keep the existing PostgreSQL channel lease backend on every node. Do not mix
  PostgreSQL and Redis lease backends during a handoff.
- Caddy must already run `stream_close_delay 5m` on every API proxy route. Adding
  it for the first time can reconnect existing sockets. Caddy normally closes
  WebSockets on reload; the option delays that close, not indefinitely preserves
  sockets. [Caddy streaming contract](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy#streaming).
- Route **every** client through a stable proxy endpoint. On s90 this includes
  public `:45671`, local daemon `127.0.0.1:45673` and frontend
  `REMOTE_API_URL=http://caddy:45673`. The backend must no longer own the host
  `:45673` binding. Switching only the public route leaves local callers on old
  code. The one-time proxy/Redis bootstrap needs a controlled restart.
- The active and candidate images must have identical migration file contents.
  Review mixed-version API, workers and transient session compatibility as well;
  matching schema is necessary but not sufficient. The tool requires explicit
  `--confirm-compatible` for this review. No automatic production migration runs.
- Caddy admin API must remain container-local at port 2019. This tool expects the
  supplied config file mounted at `/etc/caddy/Caddyfile`, one common Docker
  network and simple `reverse_proxy [@backend] alias:8080` blocks. Unsupported
  layouts fail closed. It validates the currently **loaded** delay as well.

## Prepare and switch

Use the machine's approved Python interpreter (on s90,
`$HOME/miniconda3/envs/hungyu/bin/python`). Inspect exact running names and local
image IDs before invoking. `--active-upstream` is the current Caddy DNS alias,
which may differ from the inspected Docker container name.

```sh
"$HOME/miniconda3/envs/hungyu/bin/python" scripts/deploy/server-handoff.py \
  --active CURRENT_CONTAINER --active-upstream CURRENT_PROXY_ALIAS \
  --candidate UNIQUE_NEW_CONTAINER --image sha256:REVIEWED_64_HEX_IMAGE_ID \
  --caddy CADDY_CONTAINER --config /absolute/path/Caddyfile --redis REDIS_CONTAINER
```

Without `--apply`, this checks running state, authenticated Redis PING, current
readiness, migration identity and proxy layout. It starts only disposable
manifest-reading image processes, with no network credentials or mounts.

After reviewing the preview, repeat with `--confirm-compatible --apply`. The
candidate inherits the active environment through a temporary owner-only file
and the same upload mounts/network. Multiline environment values are rejected.
It starts the immutable image with `/app/server`, bypassing the migrator. Only
after `/readyz` passes does the tool save a rollback config, validate Caddy, and
reload both API targets. Reload failure restores and reloads the original config.
Candidate startup failure leaves traffic unchanged and retains the candidate for
diagnosis. No old Server is automatically stopped. Record tool JSON output in
the deployment record; it contains image identities and rollback paths, no secrets.

## Verify, rollback and retire

1. Check public and local `/health` commit/start time against the candidate;
   `/readyz` must report DB and migrations OK. Check new and existing authenticated
   WS behavior, cross-node events, pending request results, and each daemon's
   advancing heartbeat. `/readyz` alone does not check Redis or session continuity.
2. Keep old Server alive for at least the configured delay (default five minutes),
   plus active request/session drain. Go HTTP shutdown allows ten seconds for
   ordinary HTTP requests; hijacked WebSockets are not transferred. Long terminal,
   SSE and other transient sessions need separate verification or an explicit
   reconnect window. Do not terminate a Server merely because the delay elapsed.
3. To roll traffic back, write the saved `Caddyfile.before-CANDIDATE` contents
   **in place** to the mounted Caddyfile, validate it, then `caddy reload` in the
   proxy container. Verify both endpoint process identities and daemon heartbeat.
   Keep the new Server alive until its sockets drain too. This rolls back routing,
   not database changes (this tool refuses migration changes).
4. Once all old caller routes and sessions are drained, explicitly retire the old
   container and reconcile the canonical Compose configuration/release record.
   The prepared candidate is a named `docker run` container with restart policy,
   not automatically a Compose service. Do not run an unrelated Compose update
   that restores the old backend alias or port binding.

Future schema upgrades require expand/contract migrations and a separately
reviewed rollback plan. The strict identity gate intentionally refuses them.

## Reproducible isolated exercise

Preload `pgvector/pgvector:pg17`, `redis:7.4-alpine`, `caddy:2.10.2-alpine` and the
reviewed Server image. The test uses synthetic credentials, a unique Docker
network, random loopback ports and disposable storage, then removes its containers,
anonymous volumes and network. It does not read production environment files.

```sh
"$HOME/miniconda3/envs/hungyu/bin/python" scripts/deploy/test-server-handoff.py \
  sha256:REVIEWED_64_HEX_IMAGE_ID
```

The test starts two real Servers with an isolated migrated PostgreSQL and
password-protected Redis, continuously samples both API routes, authenticates
real member WebSockets, switches and rolls back, and verifies existing socket
ping/pong throughout. A new-node issue write must reach the old-node socket through
Redis. It also runs the real-Redis cross-instance request claim/result and atomic
claim regression tests, rejecting skipped results. This validates same-build
handoff mechanics; it does not certify arbitrary future mixed-version behavior,
long terminal sessions, production load or the complete five-minute drain window.

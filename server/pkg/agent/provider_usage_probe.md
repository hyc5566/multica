# Direct provider usage probe

The Multica daemon uses `provider_usage_probe.py` to fetch account quota data
without starting an agent CLI or creating a model request. The Python source is
embedded in the daemon binary and executed with the runtime user's environment.

Supported providers:

| Provider | Local credential | Direct endpoint |
|---|---|---|
| Claude Code | `~/.claude/.credentials.json` (or `CLAUDE_CONFIG_DIR`) | `https://api.anthropic.com/api/oauth/usage` |
| Antigravity | macOS Keychain or `~/.gemini/...` OAuth files | `https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels` |
| Codex | `~/.codex/auth.json` (or `CODEX_HOME`) | `https://chatgpt.com/backend-api/wham/usage` |

These endpoints are provider-owned client backends rather than a stable public
quota API contract. Schema changes fail closed with a credential-free error;
the probe never substitutes a local usage cache and labels it fresh.

## Standalone use

From a source checkout:

```bash
scripts/provider-usage.sh claude
scripts/provider-usage.sh antigravity
scripts/provider-usage.sh codex
scripts/provider-usage.sh all
```

Standard output is normalized JSON compatible with `ProviderUsage`. Known auth,
network, provider-rate-limit, and local-rate-limit failures are also emitted as
normalized snapshots. Raw provider responses, OAuth tokens, refresh tokens,
account IDs, and credential file contents are never returned or logged.

The probe requires Python 3. It uses only the Python standard library. It does
not execute `claude`, `agy`, `codex`, or any other agent executable.

## Local request limit

Every outbound provider HTTP request reserves a local request slot before the
refresh starts. A Claude refresh with an expired token reserves two slots (one
OAuth exchange and one usage query). The state file contains request timestamps only and defaults to
`~/.multica/provider-usage/request-history.json` with mode `0600`; its directory
and lock file use restrictive permissions too.

Defaults:

- minimum interval: 30 seconds per provider;
- maximum: 60 logical refreshes per provider per hour.

Controlled deployments may override them:

```bash
MULTICA_PROVIDER_USAGE_MIN_INTERVAL_SECONDS=60
MULTICA_PROVIDER_USAGE_MAX_REQUESTS_PER_HOUR=30
MULTICA_PROVIDER_USAGE_STATE_DIR=/var/lib/multica/provider-usage
```

Manual refreshes and daemon-driven refreshes share the same state when they run
as the same OS user and use the same state directory. Concurrent requests for
the same provider inside one daemon share one in-flight probe. A locally rejected request
does not contact the provider and returns `status: "rate_limited"` with
`retry_after_seconds`.

## Credential boundary

- Credentials are read only on the runtime host and kept in process memory.
- Claude refresh tokens may be exchanged for an access token in memory; the
  helper does not rewrite the credential file.
- The daemon discards helper stderr and exposes only validated normalized JSON.
- Client input cannot select an endpoint, credential path, or command.
- Do not enable shell tracing around the helper or copy credentials into
  Multica configuration.

## Deterministic tests

Normalizer and limiter tests use fixtures only and make no network requests:

```bash
python3 -m unittest server/pkg/agent/provider_usage_probe_test.py
```

The script also exposes `--normalize <provider>` for fixture-driven parsing on
stdin. The daemon never uses that mode.

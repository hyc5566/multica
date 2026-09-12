# Direct provider usage probe

The Multica daemon uses `provider_usage_probe.py` to fetch account quota data
without starting an agent CLI or creating a model request. The Python source is
embedded in the daemon binary and executed with the runtime user's environment.

Supported providers:

| Provider | Local credential | Direct endpoint |
|---|---|---|
| Claude Code | `~/.claude/.credentials.json` (or `CLAUDE_CONFIG_DIR`) | `https://api.anthropic.com/api/oauth/usage` |
| Antigravity | macOS Keychain or `~/.gemini/...` OAuth files | `https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary` |
| Codex | `~/.codex/auth.json` (or `CODEX_HOME`) | `https://chatgpt.com/backend-api/wham/usage` |

These endpoints are provider-owned client backends rather than a stable public
quota API contract. Schema changes fail closed with a credential-free error;
the probe never substitutes a local usage cache and labels it fresh.

Antigravity uses explicit quota-summary groups/buckets: Google `gemini-5h` /
`gemini-weekly`, and the shared Claude/GPT pool `3p-5h` / `3p-weekly`.
`remainingFraction` is remaining, not used; `window` identifies the period.
Disabled, missing, invalid or amount-based values never become percentages.
Do not infer periods from reset timestamps or fill a missing window with 100%.
See [source evidence and rebuild rules](../../../docs/agy-quota.zh-tw.md).

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

When the daemon's `SSL_CERT_FILE` contains a private Multica Server CA, the
provider HTTPS context retains it and additionally loads existing OpenSSL
public CA defaults. Hostname verification and `CERT_REQUIRED` stay enabled;
missing public roots fail normally rather than disabling TLS. This applies to
the fixed provider HTTPS endpoints, not to the daemon's Server transport.

Codex responses without any usable numeric quota window are probe errors, not
successful empty snapshots, so callers retain the last successful observation.

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
as the same OS user and use the same state directory. A locally rejected request
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

## Task checkpoint consumers

The daemon may call this probe immediately before and after an agent backend
run. Those callers coalesce requests by provider account, reuse a successful
observation for at most 30 seconds, and impose a shorter outer deadline than
the probe's standalone deadline. A timeout, authentication failure, local rate
limit, schema mismatch, or report failure is observation metadata only and must
not prevent the task from starting or reaching its original terminal state.

The Server persists only this normalized structure. It does not persist the
credential file path, OAuth material, provider account identifiers, raw
responses, or raw error bodies. Task reports may annotate normalized windows
with `scope` and `model_match`; those annotations are derived locally and are
never treated as provider payload fields.

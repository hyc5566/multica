# Taiwan Traditional Chinese maintenance

The `zh-tw` branch is the long-lived integration branch for the Taiwan
Traditional Chinese edition. `upstream/main` remains the source of truth for
product behavior. Taiwan-only changes should stay in small, purpose-specific
commits so the edition can be rebuilt when an upstream rebase becomes more
expensive than replaying the customization.

## Customization layers

Apply these layers in order when rebuilding the branch:

1. UI copy: convert `packages/views/locales/zh-Hans/*.json`, landing-page copy,
   Taiwan-facing web metadata, and generated workspace names. Keep the internal
   `zh-Hans` locale key for API and storage compatibility, but expose `zh-TW` in
   HTML and platform metadata.
2. Desktop identity: use `apps/desktop/electron-builder.zh-tw.yml` to give the
   local edition its own product name, bundle identifier, executable name,
   protocol, and user-data directory. It must be installable beside upstream
   Multica.
3. Generated backend copy: localize Mika and onboarding content created by the
   server. Code comments remain English.
4. Local build tooling: use `scripts/build-desktop-zh-tw-macos.sh` for an
   Apple-Silicon, ad-hoc-signed build. Build from a checkout below
   `/mnt/data-home/hungyu/multica-zh-tw` so dependencies, caches, binaries, and
   packaging outputs do not pollute the primary source checkout.
5. CLI copy and regression tests: keep user-visible daemon hints and assertions
   aligned with the Taiwan locale.

Do not fold unrelated local product features into the localization commits.
Develop each feature from `zh-tw` on its own branch, review it through a PR, and
merge it back into `zh-tw` only after verification.

## Provider usage feature layer

Provider quota display is a Taiwan-edition feature, not part of the locale
patch. Rebuild it as two small commits after the localization layers:

1. Direct daemon probes: replay the self-contained provider probe files under
   `server/pkg/agent/`, the narrow daemon dispatch hook, and their fixture-only
   tests. The probe reads local OAuth credentials and calls fixed Codex,
   Claude, or Antigravity endpoints without starting an agent task. See
   `server/pkg/agent/provider_usage_probe.md` for the credential and rate-limit
   boundary.
2. Refresh coordinator and presentation: add the provider-usage snapshot
   migrations, `server/internal/handler/runtime_provider_usage_snapshot.go`,
   `server/cmd/server/provider_usage_refresh_job.go`, the one pending-work
   protocol value, the cache-only GET route, and the Agent usage component.

Keep these invariants when replaying onto a newer upstream:

- a daemon reconnect and the five-minute Server scheduler may request a probe,
  but both pass through the same database bucket reservation;
- one built-in provider account is keyed by daemon plus provider, while a
  custom runtime profile has its own key;
- provider failures update attempt/error metadata but never replace the last
  successful snapshot;
- opening an Agent page and periodic reads use only the Server snapshot; the
  usage card refresh button posts the existing throttled provider-usage request
  and polls completion, then reads the durable result without creating a task;
- refresh stays limited to the same five-minute bucket; a completed request is
  not proof of a new observation. Show full observation dates, stale/error
  metadata, and hide percentages for windows whose reset time has passed;
- the external provider HTTPS probe adds OpenSSL public roots when daemon
  SSL_CERT_FILE supplies a private Server CA. Preserve that private CA and keep
  hostname/CERT_REQUIRED verification; never disable TLS to recover quota;
- reject Codex responses without a usable percentage window instead of replacing
  the last-known-good snapshot with an empty partial result;
- the page can show the last successful snapshot while a runtime is offline;
- all provider payloads are normalized and validated before persistence, and
  no OAuth material is sent to the Server.

The handwritten coordinator queries are intentionally kept in the Taiwan-only
handler file. Upstream integration points are limited to scheduler
registration, reconnect notification, route registration, one protocol kind,
and the Agent usage component. During a future rebuild, prefer adapting those
small seams to the latest upstream contracts over copying old surrounding
files.

### Task quota checkpoint layer

Replay the task checkpoint feature after the two provider-usage commits above:

1. Add the append-only `provider_quota_observation`, `task_quota_checkpoint`,
   and `provider_quota_rollup` migrations and their concurrent indexes.
2. Reconnect the two narrow runner hooks around `runner.run`, the daemon
   coordinator in `server/internal/daemon/task_quota.go`, and the daemon report
   route. Probe timeout, cache and report failures are all fail-open; none may
   alter the task result.
3. Reconnect checkpoint hydration to the full issue task-history response and
   the quota section in the issue usage dialog. The UI comparison helper is the
   authority for same-observation, stale and reset-crossing semantics.
4. Register the hourly maintenance job after migrations. It retains raw
   observations for 180 days, hourly rollups for 13 months, and daily rollups
   plus task checkpoint boundaries for 25 months.

Task token rows are exact run accounting. Quota checkpoints are account-level
observations and must never be labelled as the task's own consumption. Keep the
overlap count and external-activity warning in every report/export. Model
matching is exact only when the provider supplies a stable model identifier
(currently Antigravity); Claude is account-shared and Codex named buckets stay
`unknown` rather than being guessed from display text.
## Desktop icon assets

macOS uses `apps/desktop/build/icon.icns` for the packaged app. Development and
the runtime Dock override use `apps/desktop/resources/icon.png`; the 1024px
`apps/desktop/build/icon.png` and the files below `build/icons/` must remain
visually aligned with them. Both electron-builder configurations pin the ICNS
path explicitly so packaging cannot silently select a stale PNG instead.

The September 2026 correction aligns the artwork with Apple Human Interface Guidelines (HIG):
in a 1024x1024 canvas, the standard squircle grid is 824x824 with 100px margins on all sides,
centered precisely at (512, 512). This aligns Multica seamlessly with macOS system apps
(Finder, System Settings, Safari, etc.) in Command-Tab, Dock, and Launchpad.

To rebuild the complete PNG and ICNS set on macOS, run the generator with `--hig-standard`:

```bash
scripts/generate-desktop-macos-icons.sh ./multica-icon-source.png --hig-standard
```

The generator preserves the 1024px canvas, writes every standard and Retina
ICNS representation, refreshes the packaged/runtime PNGs and Linux raster
sizes, and losslessly optimizes each PNG. Inspect the resulting icon beside
system apps in Dock, Finder, and Launchpad before committing. At minimum, also
verify the 128px and 256px PNGs for clipping and compare the ICNS 1x/2x frames.
The Windows ICO is intentionally not regenerated by this macOS-only alignment
workflow.
## Upstream update workflow

```bash
git switch main
git fetch --prune upstream
git merge --ff-only upstream/main
git switch zh-tw
git rebase main
```

Resolve locale conflicts semantically:

- preserve the newest upstream JSON shape, keys, interpolation variables, and
  product meaning;
- retain existing natural Taiwan wording when the source meaning is unchanged;
- translate newly added or materially changed values into Taiwan Traditional
  Chinese;
- never restore keys or code paths deleted upstream;
- run JSON parsing, conflict-marker checks, focused i18n tests, typecheck, and
  build checks before deployment.

If the rebase is no longer economical, create a fresh `zh-tw` from the updated
`main` and replay the customization layers above as atomic commits. The old
branch is a reference, not an authority over the new upstream schema.

## Feature and release workflow

Create local features from the integration branch:

```bash
git switch zh-tw
git switch -c feature/<short-name>
```

After implementation and review, open a PR targeting `zh-tw`. Following merge,
build the server/CLI and macOS client from the resulting `zh-tw` commit. Record
the source commit, artifact checksum, target host, installed path, service or
LaunchAgent identity, and rollback artifact in the deployment record.

## Deployment boundaries

### Chat per-run usage (HYCLV-27)

Chat message list and pagination endpoints hydrate each assistant reply with
its task's `usage` and `quota_checkpoints` after session authorization.
Reuse `hydrateAgentTaskUsage` for batched provider/model token rows and
`summarizeTaskUsage` for totals, including input, output, cache read and cache
write tokens. Missing usage stays unknown rather than zero.
`ChatTaskQuota` in shared Views renders token details alongside the existing
account-level quota comparison. Chat completion already refetches message
queries; session quota summaries also need their detail query invalidated.
Session detail also exposes chronological `task_usage` entries so the summary
reuses `ChatTaskQuota`, including runs with missing usage or checkpoints.
Antigravity task/chat checkpoint projections select only the recorded
`requested_model`: exact IDs first, then Gemini effort variants' advertised
`-tiered` bucket (marked shared). Missing identities produce no model window;
do not guess from equal percentages. Shared account observations stay intact.

Antigravity requires an updated daemon and agy >= 1.1.8: the adapter requests
`--output-format stream-json`, streams `step_update.text_delta`, and records
only terminal `result.usage`. Input includes cached input, so subtract cache
reads when mapping into Multica's disjoint counters; output already includes
thinking. Never add step usage or thinking a second time. See the
[official headless contract](https://www.antigravity.google/docs/cli/headless/).
Earlier plain-text daemon runs have no persisted token usage and cannot be
backfilled from quota percentages. Server and Web/Desktop must also be updated;
there is no new dependency or database migration.

- Linux server binaries and the running development/self-host environment are
  separate from the Apple-Silicon desktop artifact.
- The desktop app bundles its matching `multica` CLI/daemon binary; keep their
  source commit identical.
- Stage M2 artifacts in `~/hyc-workspace/multica-zh-tw` before installation.
- Keep service definitions under the user's home-backed deployment tree and
  expose them through explicit symlinks only when the host's service manager
  requires a conventional location.
- Never overwrite a running deployment without first recording its executable,
  process identity, version, and rollback path.

### Desktop artifact retention

- Keep exactly two installable Desktop archives in the controlled checkout's
  `artifacts/` directory: the current build and its immediate predecessor.
  `scripts/build-desktop-zh-tw-macos.sh` enforces this only after a new archive
  passes build and codesign verification.
- Keep one installed app at `/Applications/Multica 繁中版.app` and one previous
  app in the controlled rollback directory. Do not also retain a second copy of
  the installed app in rollback.
- Remove completed staging checkouts and their `node_modules`/`dist-zh-tw`
  directories after deployment verification. Dependency caches are
  reproducible and may be pruned when no build is running; never remove daemon
  workspaces, profiles, logs, databases, or the one retained rollback app as
  cache cleanup.

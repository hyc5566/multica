# Taiwan Traditional Chinese maintenance

The `zh-tw` branch is the long-lived integration branch for the Taiwan
Traditional Chinese edition. `upstream/main` remains the source of truth for
product behavior. Taiwan-only changes should stay in small, purpose-specific
commits so the edition can be rebuilt when an upstream rebase becomes more
expensive than replaying the customization.

## Server deployment procedure

For Taiwan-edition Server updates, follow the canonical
[繁體中文版 Server 平順切換維運程序](server-handoff.zh-tw.md).
Preserve that document, the `CLAUDE.md` reading requirement and `scripts/deploy/`
when rebuilding this edition. Deployment state and authorization must be checked
for each operation; a tested candidate is not proof of production initialization.

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
6. LAN trust: pass the daemon's `MULTICA_CA_CERT_FILE` into the explicit task
   environment so macOS agent CLIs and Codex shell tools retain the bundled CA;
   keep agent `custom_env` overrides of the `MULTICA_*` namespace blocked.

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
- manual refresh bypasses the five-minute scheduling bucket but shares a rolling
  60-second database cooldown per probe target with all callers. Honor longer
  provider Retry-After deadlines. Return `refresh_available_at` on cache reads;
  show an inline countdown and keep the last successful quota visible during
  cooldown or refresh. Do not auto-submit when the countdown ends. A completed
  request is not proof of a new observation. Show full observation dates,
  stale/error metadata, and hide percentages for expired reset windows;
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

### Antigravity explicit quota-summary layer (HYCLV-80)

Replay the direct `retrieveUserQuotaSummary` probe, the Antigravity UI pool helper,
translations and the task checkpoint pool selection together. Preserve explicit
five-hour/weekly windows, unknown values and provider-confirmed shared bucket
IDs. Do not replay agy-hud's reset-time inference or missing-value defaults.
See [source evidence, tests and rebuilding](agy-quota.zh-tw.md).

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

Verify the host, remotes, clean source state and production path isolation first.
Record the old local/remote tips and create non-overwriting backup refs. Fetch
upstream and pin its full commit. Fast-forward local `main` only when ancestry
allows it; updating remote `main` also requires task authorization. Rebase a new,
unpublished candidate in an isolated worktree, keeping the original `zh-tw`
and backups untouched. Record the old/new bases and compare every replayed
feature, including merge-only resolutions. Never rewrite shared `zh-tw` merely
because a local rebase succeeded; obtain human review and explicit history
rewrite authorization before a push bound to the expected remote OID.

### September 14, 2026 integration baseline (HYCLV-88)

The candidate moves from `d8fa885d26acbdecde49010276e905cc9de5a056` to
`aac85ab263a6080d577fa62f02c08cf61c9fd7d6`, replaying the 52 non-merge Taiwan
commits through `2e1c0a33343325232c09f63453b235e48789e789`.

- Preserve the published Taiwan quota migration stems 451–462. The ledger uses
  full filename stems, so renaming these non-idempotent migrations would rerun
  them against installed databases. The numbering lint permits only the 12
  exact historical Taiwan/upstream pairs; new collisions remain errors. Fresh
  installations and old Taiwan ledger upgrades must both be tested.
- Preserve upstream's four lifecycle categories and seven fixed status keys.
  Compact-board preferences remain a separate presentation setting; retain
  upstream sorting normalization and catalog/archive guards.
- Preserve explicit description saves together with upstream annotation
  selection/wrappers. Selecting a quote must not implicitly save a draft.
- Upstream now owns the Antigravity stream token parser (see below). Keep
  Taiwan quota snapshots/checkpoints, activity signals and input-format guard.
- Desktop machine-token ownership, isolated identity, CA trust, installer and
  provider refresh layers still apply; upstream has not replaced them.

This baseline is **not a same-schema deployment**: upstream migrations 469/470
rewrite lifecycle categories and icons; 475–477 reserve `triage`, including
renaming existing custom keys and saved filters. Some down migrations refuse
reversal. The existing same-schema handoff tool cannot establish rollback safety
for this candidate. Before deployment, separately review the upstream
[status rollout](issue-status-lifecycle-rollout.md), migration SQL, real data
impact, database backup/restore and application compatibility. No production
migration or deployment is authorized by candidate preparation.

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

The Antigravity adapter requests `--output-format stream-json`. As of upstream
`24e200dca` in the integration baseline above, preserve its DONE-step usage
aggregation, deduplicated by step index; use terminal usage only as the fallback
when step usage is unavailable. Resumed terminal results can include prior
conversation totals. Map the provider's input/output/cache buckets directly;
do not subtract cache reads or add thinking again. Stream only agent-response
text, preserve structured failure/trailing-network handling, and retain the
Taiwan activity signal for textless step updates and the `--input-format` guard.
The older Taiwan terminal-only/input-minus-cache parser is superseded. Its
replacement is covered by the upstream Antigravity tests and Taiwan activity
and custom-argument assertions, without running an authenticated agent.
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
# 介面用語與入口語系（HYCLV-64）

繁中介面中的 agent 一律保留英文 `Agent`，列表標題可用 `Agents`；不翻成「智能體」「智慧體」「助理」或「代理」。此規則包含 Web／Desktop 共用 locale、入口頁、內建上手引導、CLI 顯示文案及分支內相關說明文件；網路 proxy 的「代理」與使用者自行命名的內容不在替換範圍。

未儲存語系偏好的 Web 新訪客預設繁體中文；明確選擇英文等既有語系時保留 cookie 偏好。入口頁和登入頁透過 `apps/web/lib/locale-routing.ts` 共用這項決策。相容性 key `zh-Hans` 仍載入繁中資源，不更動 API／資料庫語系 enum。重建時檢查首頁、登入頁、導航與 Agent 頁，並核對翻譯 key 和插值不變。

## Uniform board layout layer (HYCLV-43)

To rebuild this optional Taiwan-edition UI feature on a fresh upstream:

1. Add `boardLayout: "compact" | "default"` (default: `"default"`) and its setter
   to `packages/core/issues/stores/view-store.ts`. Persist it through the existing
   view-store partializer; restore missing or invalid values as `"default"`.
   The existing surface registry isolates the preference by workspace and view.
2. Keep the shared width mapping in that store: compact 220px, default 280px.
   220px is the compact preset's lower bound; there is no per-column resizing.
3. Add the two pressed-state buttons under Display in `issues-header.tsx`, only
   for board and swimlane. Localize `display.layout_*` in all four issue bundles;
   the Taiwan edition's `zh-Hans` bundle reads `版面`, `緊緻`, `預設`.
4. Use the width in `board-column.tsx`, the board drag overlay (subtract 24px for
   existing column/card padding), and `swimlane-view.tsx` (grid, track and overlay).
   Keep all columns equal, even when grouping by assignee/project/property.

This changes only shared Web/Desktop presentation. It does not change table/list
widths, server data, filters, migrations or deployment configuration. The hidden
columns panel is a separate control and keeps its existing width. Revert the
feature commit to remove it; stale persisted layout values are harmless to older
clients. Validate with the layout/store, board interaction and swimlane suites,
locale parity, and `pnpm typecheck`. Before release, visually check long titles,
large counts, horizontal scrolling and drag/drop at both widths.


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

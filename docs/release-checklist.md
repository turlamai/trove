# Release Checklist

A release may carry the "stable" label only when every step below passes.
Development previews may cut earlier, but must not claim data safety beyond
what is verified.

## 1. Build & CI

- [ ] `main` green: daemon tests (linux), frontend typecheck+build, txz
      dry-run with checksum verification
- [ ] Clean checkout → `./scripts/build.sh <version>` reproducible
- [ ] `SHA256SUMS` matches artifacts

## 2. Install lifecycle (real Unraid machine)

- [ ] Install from `.plg` URL: page appears under Tools, daemon running
- [ ] Read-only state confirmed; configure data dir; write ops unlock
- [ ] Upgrade from previous version: settings kept, journal kept
- [ ] Uninstall: daemon stopped, package removed, settings kept, data dir
      untouched
- [ ] Reboot: page restored, daemon auto-started (event hook), journal
      intact, interrupted jobs surfaced as retryable

## 3. Safety matrix (specification §10; evidence in `docs/acceptance/`)

- [ ] T03/T04 namespace mixing refused (share↔disk, share↔pool), sources intact
- [ ] T05 same-file alias refused
- [ ] T06 traversal / encoded / unicode / long paths rejected
- [ ] T08 cross-share move completes with integrity intact
- [ ] T10 disk-full: source preserved, clear failure
- [ ] T13 array stop mid-job: safe stop, no orphaned partial files
- [ ] T14 conflict policies behave as planned (skip/rename/overwrite-on-confirm)
- [ ] T15 recursive target refused
- [ ] T16 protected shares refused by default
- [ ] T18 unauthenticated / CSRF-forged / cross-origin writes rejected
- [ ] T19 hostile file names never execute as markup
- [ ] T20 USB-only / missing data dir → read-only mode with banner

## 4. Release publication

- [ ] Tag `vX.Y.Z` → release workflow → GitHub release with `.txz`,
      rendered `trove.plg`, `SHA256SUMS`
- [ ] `trove.plg` on `main` updated by the release workflow
- [ ] Changelog section rendered in the release notes
- [ ] Post-release: install URL test in a scratch Unraid VM or the target
      server (with consent), then announcement

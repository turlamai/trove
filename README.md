# Trove

A storage-aware file manager for the Unraid WebGUI — shares-first browsing, an optional dual-pane workflow, and server-side background jobs with staging → verify → atomic commit semantics. It refuses, by design, the operations that most often lose data on Unraid: mixing logical shares with physical disk views.

[![CI](https://github.com/turlamai/trove/actions/workflows/ci.yml/badge.svg)](https://github.com/turlamai/trove/actions/workflows/ci.yml) [![License](https://img.shields.io/badge/license-MIT-blue)](LICENSE) [![Version](https://img.shields.io/github/v/release/turlamai/trove?color=C15F3C&label=version)](https://github.com/turlamai/trove/releases/latest)

**English** · [中文](README.zh-CN.md)

> **Development preview.** Not yet accepted on a real machine against the
> full safety matrix ([docs/release-checklist.md](docs/release-checklist.md)).
> Install on test machines first — or read
> [docs/safety.md](docs/safety.md) and decide for your own server.
> Until a job data directory is configured, Trove runs **read-only**.

## Start here

Trove is a native plugin: it installs through Unraid's plugin mechanism, appears under **Tools → Trove**, follows the WebGUI theme (light/dark), and never touches the built-in file manager.

What it does:

- **Shares-first browsing** — logical shares in the navigation; physical disks and pools appear in expert mode, read-only by default
- **Single / dual pane** — dual-pane with independent paths, drag or F6 transfers, and a visible target at all times
- **Background jobs** — copy/move/delete/hash run in a root-owned daemon with a persisted journal; closing the page changes nothing
- **Storage-safety checks** — share↔disk namespace mixing is refused with an explainer (not a warning you can click through); same-file aliases, recursive targets and source mutation are caught server-side
- **Conflict handling** — per-item skip / overwrite / keep-both decisions with source↔target comparison; overwrite is never the default
- **Inspector** — verified metadata only; what cannot be verified is shown as "unknown"
- **zh-CN / English UI**, keyboard-driven, follows the Unraid light/dark theme

What it deliberately does not do (yet): upload, previews, thumbnails, recycle bin, and any write into `appdata`/`domains`/`system`/`isos` without explicit confirmation. See [docs/compatibility-matrix.md](docs/compatibility-matrix.md).

## Install (development preview)

1. Take the `trove.plg` URL from the [latest release](https://github.com/turlamai/trove/releases/latest):
   `https://raw.githubusercontent.com/turlamai/trove/main/trove.plg`
2. Unraid WebGUI → **Plugins → Install Plugin**, paste the URL.
3. **Settings → Trove**: set the *job data directory* (a folder on the array or a pool, e.g. `/mnt/user/trove-data` — never the USB flash). Until then the plugin is read-only.
4. Open **Tools → Trove**.

Verify checksums before installing anything:
`shasum -a 256 --check --ignore-missing SHA256SUMS` with the release assets in one directory. The plugin manifest pins the package SHA-256 as well.

## How it stays safe

Every operation is planned server-side before it runs: the daemon walks the source tree, detects conflicts and risks, and issues a short-lived confirmation token. Execution stages every file into the target directory, fsyncs, verifies, then commits atomically; a move deletes its source only after verification and identity re-checks — a source that changed mid-flight is kept, never deleted. Path traversal is contained by `openat2(RESOLVE_BENEATH)` in the kernel; if that is unavailable, writes are refused.

Details: [docs/architecture.md](docs/architecture.md) · [docs/threat-model.md](docs/threat-model.md) · [docs/safety.md](docs/safety.md)

## Repository layout

```text
trove/
├── daemon/          Go daemon (roots / resolver / planner / executor / jobs / api)
├── frontend/        Preact + Vite SPA (no CDN dependencies)
├── plugin/          .plg manifest + WebGUI files (.page, PHP gateway, hooks)
├── scripts/         build & packaging (txz, SHA-256, icon)
├── docs/            architecture, threat model, API contract, compatibility matrix,
│                    release checklist, ADRs, product specification
└── .github/         CI (test + package dry run) and release workflow
```

## Development

```sh
# daemon
cd daemon && go test ./... && go vet ./...

# frontend (dev server proxies to a local daemon)
cd frontend && npm install && npm run dev

# full package (version required)
./scripts/build.sh 0.1.0
```

Run the daemon locally against a synthetic root tree (never your real data):

```sh
mkdir -p devroot/share1 devroot/share2
cd daemon && go run ./cmd/troved --dev-roots ../devroot --dev-tcp 127.0.0.1:8787 \
    --socket /tmp/troved.sock --token-file /tmp/troved.token \
    --config /tmp/trove.cfg --data-dir /tmp/trove-data
```

The Linux syscall layer (openat2 containment, executor semantics) is covered by tests that run on Linux CI.

## Status & roadmap

- **V1 (this preview)** — browsing, dual-pane, safe operations, task center, settings, i18n
- **V1.5 (planned)** — upload, previews (sandboxed), batch rename preview, archives, configurable trash (only after its own safety review)
- Physical-view writes stay disabled by default; cross-view mixing stays refused unconditionally

## License

[MIT](LICENSE)

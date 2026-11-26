# Architecture

Status: design baseline implemented as development preview. Claims about
Unraid behavior are verified against the target server (Unraid 7.x) during
acceptance testing (docs/compatibility-matrix.md).

## Components

```
Unraid WebGUI (nginx, same origin)
├── trove.page / trove-settings.page      Tools & Settings registration
├── /plugins/trove/web/app.{js,css}       static SPA bundle (no CDN)
└── /plugins/trove/include/api.php        PHP gateway (auth, CSRF, routing)
                 │  Unix domain socket /run/trove/troved.sock
                 │  peer credentials (root) + per-start token (0600)
                 ▼
        troved (Go daemon, root)
        ├── roots        mount-backed root registry (share/disk/pool)
        ├── resolver     openat2(RESOLVE_BENEATH) path confinement
        ├── planner      operation matrix, conflicts, plan tokens (HMAC)
        ├── executor     staging → fsync → verify → atomic commit
        ├── jobs         queue, state machine, journal, cancellation
        └── store        state.json + events.jsonl in the data directory
                 ▼
        Unraid shares / array disks / pools
```

## Why this shape

**Typed roots instead of path strings.** The frontend addresses every
operation as `(rootRef, relativePath)`. Absolute paths from clients are never
executed; the daemon's registry only contains mounts it discovered under
`/mnt`. Protected system trees (`/boot`, `/etc`, …) are structurally
unreachable — not merely forbidden by a check.

**Kernel confinement.** Every resolution goes through
`openat2(RESOLVE_BENEATH | RESOLVE_NO_MAGICLINKS)` relative to the root's
directory descriptor. Symlinks cannot walk out of the root, and there is no
TOCTOU window between "check" and "act" because traversal and act use the
same verified descriptors (`openat`, `renameat2`, `unlinkat`). If the kernel
lacks openat2 the daemon refuses writes — there is no unsafe fallback.

**Gateway is thin on purpose.** PHP cannot be made a safe place for
long-running filesystem work (php-fpm request lifecycle). The gateway
authenticates, enforces CSRF/origin, routes a fixed whitelist of commands and
forwards JSON; everything else happens in the daemon, which authenticates the
gateway by socket peer credentials plus a per-start token.

**Plan → confirm → execute.** Write operations require a dry run (the plan),
which returns counts, conflicts, risks and a short-lived HMAC confirmation
token bound to the plan's content. Submission consumes the token
(single-use). The client cannot skip the plan or forge an approval.

**Journal over SQLite.** Job state is an atomically rewritten `state.json`
plus an append-only `events.jsonl` (fsynced). This keeps the daemon
stdlib-only and the failure model simple; see ADR-0002.

## Request flow (write operation)

1. UI collects intent (drag, clipboard, dialog) — UI never sends absolute
   paths.
2. `POST plan` → daemon validates namespace matrix, protected shares,
   same-file aliases, recursive targets, conflicts, space; walks the source
   tree into an internal manifest (bounded); returns plan + token.
3. UI shows the plan (counts, sizes, target, conflict policy, risks).
4. `POST jobs {planId, token}` → queued. The worker re-resolves both roots,
   re-verifies mount identity, and executes per-file with staging commits.
5. Job states stream via polling: queued → running → verifying → completed /
   failed_retryable / needs_attention / cancelled.

## Failure semantics

- Daemon restart: non-terminal jobs become `failed_retryable`; a retry
  re-plans from the recorded source/target — the daemon never resumes a copy
  blind.
- Mid-copy crash: targets only ever appear through atomic renames of
  fully-written, verified staging files. Staging leftovers are cleaned on the
  next run.
- Array stop / unmount mid-job: mount identity checks fail closed
  (`MOUNT_CHANGED`); the job stops, sources stay intact.
- No data directory configured: the whole job engine is disabled — the
  plugin browses read-only rather than running jobs without a journal.

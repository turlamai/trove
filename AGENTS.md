# Agent working rules for this repository

For humans and agents changing Trove. The product specification
([docs/specification.md](docs/specification.md)) is the authority; these are
the working rules distilled from it.

## Red lines (never violate)

1. Never mix `/mnt/user` with `/mnt/diskN` or pool views in one operation.
   There is no override, no dialog, no config flag — by design.
2. No write path exists without the full backend chain (WebGUI auth → CSRF →
   socket peer cred + token → typed-root planner). Hiding UI buttons is not
   a safety control.
3. A move deletes source data only after copy verification and per-file
   identity re-checks. Changed sources are kept, never deleted.
4. Never modify Unraid core files, the built-in file manager, or the system
   task tray — even when it would be convenient.
5. Never claim "stable" or "safe for production" without real-machine
   acceptance evidence (docs/release-checklist.md).

## Engineering rules

- **Fail closed.** Any unverified condition (mount identity, kernel feature,
  journal availability, decision value) denies the operation. No unsafe
  fallbacks.
- **Safety logic is unit-tested.** Planner/namespace/alias decisions must be
  testable without a filesystem (see `internal/planner/planner_test.go`);
  kernel-layer tests run on Linux CI.
- **The PHP gateway stays a dumb pipe.** Fixed route table, no business
  logic, no new shell commands. If a feature seems to need PHP logic, it
  belongs in the daemon.
- **No absolute paths from clients.** Everything is (typed root, relative
  path). If you find yourself passing an absolute path across the API, stop.
- **No new dependencies in the daemon** without an ADR; frontend stays
  framework-light (Preact + hand-rolled UI, no CDN).
- **Docs move with behavior.** API changes update docs/api-contract.md;
  behavior changes update the compatibility matrix and safety docs in the
  same PR.
- **Main stays green.** CI covers daemon (linux), frontend and a txz
  dry-run; broken CI is fixed or reverted immediately.

## Evidence discipline

Every claim about Unraid behavior in docs needs a pointer to the test or
acceptance artifact that backs it. "It should work" is not a status;
"verified by T13 on <date>, see docs/acceptance/…" is.

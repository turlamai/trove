# Safety Rules (the red lines)

These are the five invariants from the product specification, expressed as
enforced code paths. Each links to the component that enforces it and the
tests that pin it.

1. **No namespace mixing.** One operation touches one root kind. Shares
   (`/mnt/user`) and physical views (`/mnt/diskN`, pools) are never combined
   as source and target — they can be two views of the same data. There is
   no setting, dialog, or client parameter that overrides this.
   → `planner.checkNamespaces` · tests: `TestMatrix*Refused`, `TestMatrix*Allowed`

2. **No writes without a verified backend.** Every write passes WebGUI auth,
   gateway CSRF/origin checks, daemon socket peer-credential + token auth,
   and the planner's typed-root validation. Hiding a button in the UI is
   never the control.
   → `api.php`, `api/server.go` token middleware, `listener_linux.go`

3. **A move never deletes an unverified source.** Sources are deleted only
   after copy verification and per-file identity re-checks; anything that
   changed is kept and the job enters `needs_attention`.
   → `executor.deleteSourceTree` · tests: `TestMoveSourceChangedNeverDeleted`

4. **No modification of Unraid core.** The plugin installs only its own
   files under `/usr/local/emhttp/plugins/trove/`, `/usr/local/sbin/troved`
   and `/boot/config/plugins/trove/`. It never patches WebGUI files, the
   built-in file manager, or the system task tray.

5. **No stable release without real-machine acceptance.** Releases are
   marked development previews until the specification §10 matrix
   (docs/compatibility-matrix.md) passes on a real Unraid machine, with
   before/after evidence.

## Fail-closed posture

- No `openat2` support → writes refused (no unsafe fallback).
- Job data directory missing/unwritable → whole write path disabled
  (read-only browsing).
- Mount identity changed between plan and execution → `MOUNT_CHANGED`,
  operation aborted.
- Unknown conflict decision values → treated as *skip*, never overwrite.
- Planner scan truncated (huge trees) → `estimate` flag surfaced in the UI;
  execution re-verifies everything it actually touches.

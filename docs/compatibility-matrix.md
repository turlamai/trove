# Compatibility Matrix

Verified against: **pending real-machine acceptance** (target server runs
Unraid 7.x; specification §10 test matrix is the gate). Anything below is a
design claim until the corresponding test passes.

## Unraid versions

| Version | Status |
| --- | --- |
| 7.4.x (target baseline) | primary target; acceptance pending |
| 7.0 – 7.3 | expected compatible (page/plg mechanisms unchanged); untested |
| < 7.0 | unsupported (`min="7.0.0"` in the plugin manifest) |

Kernel requirement: openat2 (Linux ≥ 5.6) — Unraid 7 ships 6.x. Without it
the daemon refuses all writes (fail closed, `UNSUPPORTED`).

## Filesystem / storage views

| Path view | Browse | Copy/Move inside view | Cross-view operations |
| --- | --- | --- | --- |
| User shares (`/mnt/user/<share>`) | yes | yes (background jobs) | refused (`NAMESPACE_MIX_FORBIDDEN`), no override |
| Array disks (`/mnt/diskN`) | expert mode | disabled by default (`PHYSICAL_WRITE_DISABLED`); admin can enable | refused to/from shares |
| Pools (`/mnt/<pool>`) | expert mode | disabled by default; admin can enable | refused to/from shares |
| System shares (appdata/domains/system/isos) | yes | refused by default; "ack" mode requires explicit confirmation | — |
| /boot, /etc, /usr, … | unreachable | unreachable | unreachable |

Notes: "path changed" never equals "file migrated to target disk". Trove
never promises physical placement — that is shfs/Mover's domain. Cross-share
moves within `/mnt/user` use atomic rename when both sides are on the same
FUSE mount (same `st_dev`); otherwise they run as copy → verify → safe
source delete, with the source kept on any identity mismatch.

## File types

| Type | Copy | Move | Delete | Notes |
| --- | --- | --- | --- | --- |
| Regular file | yes | yes | yes | mode + uid/gid + mtime preserved |
| Directory tree | yes | yes | yes | children-first delete; unknown leftovers keep the directory |
| Symlink | skipped w/ warning | — | link itself only | never followed out of the root |
| Hardlinked file | copied as independent file | rename preserves the link | yes | `HARDLINK_SPLIT` risk note |
| FIFO / socket / device | skipped w/ warning | skipped | refused (kept) | `UNSUPPORTED_TYPE` |
| Sparse files | copied dense (may use more space) | rename keeps sparseness | yes | documented limitation |
| ACL / xattr | not preserved (V1) | preserved by rename | — | documented limitation |

## Browsers

| Browser | Status |
| --- | --- |
| Chrome/Edge ≥ 100, Firefox ≥ 100, Safari ≥ 15 | target |

## Known limitations (V1)

- Upload is not implemented (planned V1.5); download is single-file only.
- No thumbnails; the inspector shows metadata, not media previews.
- Directory listings are fetched in pages of 500; the UI window-renders, but
  total counts of very large directories are exact only after full scroll.
- Search is name-in-subtree with scan caps (20k entries / 8 s), not an index.

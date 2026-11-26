# Threat Model

Assets: user file data on shares/disks/pools; Unraid system configuration;
WebGUI session authenticity.

Adversaries (specification §8.1):

- **A1 — malicious web content**: a page in the user's browser (not the
  Unraid GUI) attempts requests against the Trove API (CSRF) or injects
  content into the UI (XSS).
- **A2 — malicious file/dir names**: names containing HTML, control chars,
  `..`, NUL, homoglyphs, extreme lengths; zip-slip-style payloads embedded in
  future archive handling.
- **A3 — local unprivileged processes**: anything on the NAS trying to reach
  the daemon socket or forge gateway requests.
- **A4 — storage races**: unmounts, device swaps, symlink swaps inside
  shares, files modified by Docker/VM/Mover during operations, concurrent
  Trove jobs targeting the same directory.
- **A5 — supply chain**: tampered plugin packages or update feeds.

Out of scope: adversaries with root shell on the NAS (game over by
definition); physical attacks; malicious SMB clients on a trusted LAN beyond
the file names/content they may plant.

## Controls

| Threat | Control |
| --- | --- |
| A1 CSRF | Writes require `X-Trove-Csrf` header equal to the WebGUI CSRF token from `/var/local/emhttp/var.ini` (custom headers cannot be added by cross-site forms); same-origin enforcement on `Origin`/`Referer` when present; daemon additionally authenticates the gateway (peer cred + token), so a forged HTTP request that somehow reached the socket URL still fails at the socket. |
| A1 XSS | Preact text interpolation everywhere; no `dangerouslySetInnerHTML`; no `eval`; bundle served same-origin by nginx with `nosniff`; unsafe file names are displayed as text only. |
| A1 session | All `/plugins/trove/**` URLs sit behind Unraid's WebGUI authentication (nginx). The gateway refuses requests when the CSRF token cannot be read. |
| A2 traversal | String layer rejects `..`, absolute, backslash, NUL, control chars, invalid UTF-8, overlong names before any syscall; kernel layer enforces `RESOLVE_BENEATH` per resolution — both layers must agree. |
| A2 XSS via names | Names rendered through Preact text nodes; escaped by the framework. |
| A3 socket | `/run/trove` is 0700, socket 0600, peer-credential check (uid must match daemon owner = root), per-start token file 0600. |
| A4 aliasing | Same `(dev, ino)` at source/target refused (`SAME_FILE_ALIAS`); target-inside-source refused; mount identity (mount id + device) re-verified before every operation (`MOUNT_CHANGED` fail closed). |
| A4 source mutation | Per-file identity checks against the planned manifest; a changed source is skipped (or aborts, per policy) and **never deleted** in a move; directory deletion uses `rmdir` which fails on unknown leftovers. |
| A4 concurrent jobs | Default concurrency 1 (configurable ≤ 4); conflict decisions pause jobs rather than guessing. |
| A5 supply chain | `.plg` pins the release URL and SHA-256 of the `.txz`; releases carry `SHA256SUMS`; no unpinned `latest` fetches; build from clean checkout in CI. |

## Explicit non-goals (V1)

- No recycle bin / undo: deletion is permanent and confirmed per specification
  §4.4; the UI never shows an undo button for unverified restores.
- No preview/edit of untrusted file content in V1 (planned V1.5 feature will
  require its own sandboxing review).
- Physical-view (disk/pool) writes are disabled by default and must be
  enabled deliberately; share↔disk mixing remains refused unconditionally —
  there is no setting, dialog or override that re-enables it.

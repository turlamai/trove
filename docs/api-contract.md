# API Contract

Transport: browser → same-origin PHP gateway (`/plugins/trove/include/gateway.php?cmd=<command>`)
→ Unix socket → daemon. All bodies are JSON (UTF-8). The gateway enforces
Unraid WebGUI auth, CSRF (writes) and same-origin; the daemon additionally
authenticates the gateway.

## Conventions

- Errors use the envelope:
  ```json
  {"error": {"code": "NAMESPACE_MIX_FORBIDDEN",
             "message": "…",
             "recoverable": false,
             "requestId": "…"}}
  ```
- Codes: `UNAUTHORIZED, CSRF_INVALID, ORIGIN_REJECTED, BAD_REQUEST,
  ROOT_NOT_ALLOWED, ROOT_NOT_FOUND, PATH_ESCAPE, UNSUPPORTED_PATH,
  NAMESPACE_MIX_FORBIDDEN, SAME_FILE_ALIAS, RECURSIVE_TARGET, ARRAY_STOPPED,
  MOUNT_CHANGED, NOT_WRITABLE, PHYSICAL_WRITE_DISABLED, PROTECTED_SHARE,
  INSUFFICIENT_SPACE, SOURCE_CHANGED, CONFLICT_REQUIRES_DECISION,
  UNSUPPORTED_TYPE, IO_FAILURE, NOT_FOUND, JOB_STORAGE_UNAVAILABLE,
  PLAN_EXPIRED, PLAN_MISMATCH, DUPLICATE_REQUEST, CANCEL_DENIED, UNSUPPORTED,
  INTERNAL`.
- Root references are typed: `{"kind": "share|disk|pool", "id": "<name>"}`,
  serialized as `rootId=share:media` in query strings.

## Commands

| cmd | method | purpose |
| --- | --- | --- |
| `capabilities` | GET | version, feature switches, read-only reason |
| `roots` | GET | discovered roots (share/disk/pool) |
| `entries` | GET | paginated listing (`rootId, path, sort, dir, offset, limit, filter`) |
| `entry` | GET | single entry metadata |
| `download` | GET | stream one file as attachment |
| `search` | GET | bounded name search in a subtree |
| `plan` | POST | dry-run an operation → plan + confirmation token |
| `execute` | POST | instant action (mkdir, rename) from an approved plan |
| `jobs` | GET | job list (newest first) |
| `job` | GET | job detail (`id`) |
| `job_cancel` | POST | request cooperative cancel |
| `job_retry` | POST | re-plan and re-submit a retryable job |
| `job_resolve` | POST | submit conflict decisions `{decisions:[{path,resolution}]}` |
| `job_events` | GET | journal events since a sequence number |
| `settings` | GET | daemon settings |
| `settings_put` | PUT | update settings (validated; data dir must be under /mnt) |

## Plan / job flow

```jsonc
// POST plan
{"action": "copy",
 "source":    {"root": {"kind":"share","id":"media"}, "path": "movies/a.mkv"},
 "targetDir": {"root": {"kind":"share","id":"archive"}, "path": "movies"},
 "conflictPolicy": "ask",           // ask | skip | rename | overwrite
 "sourcePolicy": "skip",            // skip | abort (source changed during move)
 "verify": "size-and-metadata",     // size-and-metadata | sha256
 "ackProtected": false,
 "clientRequestId": "uuid"}
```

Response: plan with `items/files/dirs/bytes`, `conflicts[]`, `risks[]`,
`needsAck`, and `token` (when fully approved; HMAC-bound to plan content,
single-use, 10 min TTL).

`POST jobs {planId, token, clientRequestId}` → job record. Idempotency:
resubmitting the same `clientRequestId` returns the existing job with
`DUPLICATE_REQUEST`.

Job states: `queued → running → verifying → completed`, plus
`cancel_requested → cancelled`, `failed` / `failed_retryable`,
`needs_attention` (pending `conflicts[]` awaiting `job_resolve` decisions of
`skip | rename | overwrite`).

## Stability

The API is versioned (`/api/v1/…`) and documented here; breaking changes
require a major release and a compatibility note in
docs/compatibility-matrix.md.

# ADR-0003: openat2 confinement, fail closed

Date: 2026-11-26 · Status: accepted

## Context

Path safety can be attempted in userspace (clean strings, lstat every
component) but that leaves TOCTOU windows: a directory can be swapped for a
symlink between check and use. Linux 5.6+ provides openat2 with
RESOLVE_BENEATH / RESOLVE_NO_MAGICLINKS, enforcing containment in the kernel
per resolution. Unraid 7 kernels are ≥ 6.6.

## Decision

All traversal happens via openat2 relative to the root's directory
descriptor; final components are handled with *at() syscalls
(AT_SYMLINK_NOFOLLOW). If openat2 is unavailable, writes are refused with
`UNSUPPORTED` — there is deliberately no userspace fallback.

## Consequences

- Safety depends on kernel ≥ 5.6; documented in the compatibility matrix and
  surfaced in `/capabilities.openat2`.
- Symlinked directories inside a root can be resolved (BENEATH confines
  them), but tree walks refuse to descend through symlinks and transfers
  skip symlink entries with warnings — predictable and conservative.

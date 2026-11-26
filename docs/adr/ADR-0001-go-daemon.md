# ADR-0001: Go daemon behind a thin PHP gateway

Date: 2026-11-26 · Status: accepted

## Context

The specification recommends Go or Rust for the background engine, with an
explicit allowance to spike alternatives (§6.1). Unraid's WebGUI is PHP +
nginx; plugin pages run in that world and long-running filesystem work does
not fit the php-fpm request lifecycle.

## Decision

A single static Go binary (`troved`, stdlib + `golang.org/x/sys`) listening
on a local Unix socket, with a PHP gateway limited to auth/CSRF/routing.

## Rationale

- Static binary, no runtime to deploy or pin; cross-compiles in CI; small
  package size for the `.txz`.
- Kernel-level safety primitives we depend on (openat2, renameat2, *at()
  descriptors, SO_PEERCRED) are directly reachable from Go.
- Single goroutine-model job engine with cooperative cancellation; PHP
  would need detached child processes and ad-hoc IPC.
- Rust was viable but slower to iterate for this scope; Racket was evaluated
  and rejected: runtime deployment weight on Unraid and no openat2-level
  control without FFI — the reliability-first clause of the specification
  applies.

## Consequences

- Two languages in the plugin (Go + PHP gateway + TS frontend); the gateway
  must stay a dumb pipe (enforced by review and its fixed route table).
- CGO is disabled; SQLite is avoided (see ADR-0002).

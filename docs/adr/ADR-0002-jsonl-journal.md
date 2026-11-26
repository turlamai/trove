# ADR-0002: JSON journal instead of SQLite

Date: 2026-11-26 · Status: accepted

## Context

The specification suggests SQLite for the job journal (§5.3, §6.1) while
requiring zero external runtime weight and clean reproducible builds. The
pure-Go SQLite binding (modernc.org/sqlite) works but adds a large dependency
tree to a stdlib-only daemon.

## Decision

Persist job state as an atomically rewritten `state.json` snapshot plus an
append-only fsynced `events.jsonl` in the configured data directory, behind a
small store interface.

## Rationale

- Job counts are small (pruned at 30 days); a document snapshot is the right
  size class.
- Failure model is simple: partial writes never exist (tmp + rename + dir
  fsync); the event journal is append-only.
- The store interface can be swapped for SQLite later without touching
  planner/executor/API code.

## Consequences

- No ad-hoc SQL queries over job history; the API covers list/get/events.
- Crash-consistent by construction; recovery on start marks non-terminal jobs
  `failed_retryable` (re-plan on retry — no blind resume).

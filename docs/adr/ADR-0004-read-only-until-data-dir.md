# ADR-0004: read-only until a data directory is configured

Date: 2026-11-26 · Status: accepted

## Context

Background jobs need a persistent journal. The specification forbids
high-frequency writes to the USB flash boot device and requires that a
missing persistence layer degrade the plugin to read-only (§5.3, T20).

## Decision

Until an admin configures a data directory (Settings → Trove, must be under
`/mnt`), the daemon serves browsing/metadata/search but disables every write
path — plan/execute/jobs return `JOB_STORAGE_UNAVAILABLE`, and the UI shows a
persistent read-only banner.

## Consequences

- First install cannot lose data: the plugin is browse-only out of the box.
- The admin consciously chooses where job state lives; the daemon never
  auto-creates shares or writes to flash.
- Capabilities expose `writeOk=false` + `readOnlyWhy` so the UI states the
  reason instead of failing mysteriously.

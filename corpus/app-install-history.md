# App installation history and rollback retention

Owner: codex/01a1c9d7-d194-7222-9a81-49fbbdb020fc.
Status: implementation and migration verification in progress, 2026-11-11.

## Intent and baseline

Nico's instruction, verbatim: “keep one rollback locally, move the older
backups to X10, preserve the receipts in SQLite, verify everything before
deleting the originals, and fix the installer so this doesn’t keep happening.”

Inspection of `/Applications` found 42 distinct rollback trees (650.41 MiB
logical file bytes), 34 installation receipts (about 20 KiB), one empty
installer lock and the live 1.6.0 build 23 app. All 42 rollback signatures and
the live app passed `codesign --verify --deep --strict`. All 34 receipts name
distinct installed digests. Same version labels did not mean identical bytes.
The initial recommendation to delete old receipts was superseded by Nico's
explicit instruction to preserve history. No originals were removed during
inspection. `X10 Pro` was mounted with about 688 GiB available.

## Decisions

1. Preserve exact, schema-validated receipt bytes in an owner-only SQLite DB,
   together with indexed dates, versions and digests. Keep the app bundles as
   files. SQLite is the read authority; JSON survives only inside the historical
   receipt records, not as a second operational storage lane.
2. Keep the newest dated rollback under the private history directory. Archive
   older copies to the configured external directory. The destination path's
   SHA-256 scopes each installation under
   `~/.nicos-dev/session-pressure/app-installs/<destination-sha256>/`.
3. Make migration restartable: verify every copied tree (including file modes
   and full file content) and its signature; sync the copied files; commit and
   read back receipt bytes; check SQLite integrity and create a verified SQLite
   snapshot on X10 before any original removal. Recheck each source and target
   before removing that source. Record its verified destination and digest.
4. Keep the legacy empty lock inode for compatibility with older running
   installers. New installs lock private state and any existing legacy lock.
   They create neither dated receipts nor permanent rollback folders beside
   the app. Do not modify the live app to perform maintenance.
5. Save the archive root in SQLite. If the disk is offline or archive verification
   fails, retain extra local rollbacks and report maintenance pending. A working
   app promotion is not undone because optional archival is unavailable.
6. Install only the updated release helper as a separate immutable artifact,
   preserving the prior helper link and all existing product bundle bytes.
   Do not rebuild/promote or restart the resident, CLI, or live app for this fix.
   A later full product promotion from this source incorporates the same fix.

## Stack, provenance and commands

The existing release helper is Go and already shares the project's
`modernc.org/sqlite` dependency. The Mac uses `ditto` to preserve native bundle
metadata; `readAppIdentity` binds directory/file modes, relative paths and all
file bytes. `codesign` verifies native signatures. All copies are written to
temporary directories on the target filesystem before rename. SQLite uses full
synchronous commits and `VACUUM INTO` for consistent external snapshots.

```text
session-pressure-release app-maintain --archive-root "/Volumes/X10 Pro/NDev Pressure app history"
session-pressure-release app-maintain
```

The archive root must already exist. A missing mount never causes a new fake
`/Volumes` disk directory. The migration excludes the live `.app`; it imports
only timestamped installer receipts and rollback paths matching the installer
contract. Unexpected receipt schemas or conflicting archive contents fail
closed, preserving originals. Exact receipt bytes are stored privately, never
exported as arbitrary application log text.

## Alternatives

Scores are 1–5 for preservation, restore simplicity and operational cost;
adoption cutoff is at least 4 in each category.

| Alternative | Preservation | Restore | Cost | Decision |
|---|---:|---:|---:|---|
| SQLite metadata + app files on X10, one local rollback | 5 | 5 | 4 | Adopt |
| Delete all old receipts and bundles | 1 | 2 | 5 | Reject: loses requested history |
| Store app bundles as SQLite BLOBs | 5 | 2 | 2 | Reject: native metadata/restore complexity |
| Keep all files in Applications | 5 | 4 | 1 | Reject: repeats the clutter |

BLOB storage could be reconsidered if a portable native-bundle container and
tested metadata-preserving restore become necessary. Automated deletion of
external history is not authorized by this task.

## Proof and open items

- [x] Inventory unique backups and receipts; verify source signatures.
- [x] Race-enabled release-package tests passed on runner-3, job
  `ca9d212e-b819-4ab7-afd8-1c0940d883c3` (initial candidate).
- [ ] Final candidate tests/vet and native cross-build.
- [ ] Commit and install immutable helper; native temporary installation proof.
- [ ] Run real migration; independently verify all 42 backup identities, all
  34 receipt bytes, database integrity, external snapshot and unchanged live app.
- [ ] Confirm an idempotent retry and document final measured state.

## Journal and measurement

2026-11-11: user clarified that history must be preserved, superseding the
earlier delete-oriented recommendation. Implemented two-phase migration and
automatic archive retention. Failure tests cover corrupt copies, invalid
receipts, offline archive, signature failure, conflicting archive, missing
configuration, re-archival of the former local rollback and concurrent locks.
Seven-day telemetry lookup for `session-pressure-release` returned zero rows;
this is a coverage limitation, not proof of zero use or zero failures. The
file/database inventory and native dogfood provide direct acceptance evidence.

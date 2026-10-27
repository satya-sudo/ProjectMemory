# Phase 2 implementation and verification status

Date: 2026-10-26. Phase 2 is **implemented, pending live database verification**, not complete under its exit criteria.

## Implemented

- Local credential provisioning and bearer/session identity (Phase 2.1).
- Project capture, metadata/detail, literal title/summary search, archive/unarchive.
- Typed document, decision, question, progress, and workspace operations.
- Atomic checkpoint persistence with ownership checks, fresh client verification, request idempotency locks, project revision locks, and stored retry responses.
- Before/after history with consolidated content-update/acceptance changes.
- Consistent context snapshots and paginated read endpoints.
- Signed profile/filter/page-size/revision-bound cursors, explicit context coverage, and UTF-8-safe previews.
- Authentication, CSRF, strict JSON/query validation, bounded payloads, and sanitized HTTP errors across memory routes.

No schema changes or new runtime dependencies were needed for the project-memory endpoints.

## Local checks

Result: **104 named tests/subtests passed** in the database-independent race-enabled run, plus **3 static database-package tests passed** (migration bundle, record normalization/projection, and safe error mapping). There were no failed or skipped named tests in these targeted runs. The CLI packages currently have no direct unit tests. `go vet`, the binary build, and whitespace checks passed.

The database-independent rules and HTTP tests run against fake backend/identity implementations; they do not start Docker or connect to PostgreSQL. They cover:

- Accepted-content reset, explicit acceptance references, terminal state restrictions, and no-op rejection.
- Question resolution/reopening and progress evidence reset when an outcome changes.
- Batch operation/ref/target validation, permitted update-then-state pairs, field/size limits, canonical workspace paths, and required capture fields.
- Stable canonical request hashes and rejection before persistence.
- Cursor tampering, scope/filter binding, revision-staleness rules, restart invalidation, and Unicode preview boundaries.
- All read/write route mappings, identity propagation, required retry keys, replay headers, conflict details, and CSRF enforcement for domain writes.
- Duplicate/case-aliased JSON fields, invalid/null types, malformed/unknown/repeated query parameters, body limits, and safe storage errors.
- Static database tests for lossless integer revisions, UTC timestamp normalization, safe project projection, and sanitized storage errors.

The new integration tests compile, but compilation is not SQL execution. No Docker commands, credential provisioning CLI execution, migrations, or live database checks were run during this increment.

## Written but not run

The following opt-in PostgreSQL suites use the existing isolated-schema helper and are pending explicit user authorization:

| Test | Intended verification |
| --- | --- |
| `TestClientIdentityResolution` | Profile/client lookup, unknown client rejection, and live revocation |
| `TestMemoryCaptureUpdatesHistoryAndReplay` | Capture, initial content, successful replay, changed-key-input conflict, checkpoint updates, stale revisions, context, full documents, workspace resolution, and history |
| `TestMemoryRollbackArchiveAndCrossProjectTargets` | Rollback after partial SQL writes/unique conflict, failed-request cleanup, foreign targets, and archive/unarchive |
| `TestMemoryPaginationContextCoverageAndStaleCursors` | Large sections, complete accepted-decision coverage, correct continuation filters/page sizes, and stale document cursors |
| `TestMemoryConcurrentRetriesAndRevisionConflicts` | Eight equal-key creates produce one project; eight competing revisions produce one successful update |
| `TestMemoryLiteralSearchAndCoalescedAcceptance` | Literal SQL wildcard escaping and a single audited entity change for content update plus acceptance |

The actual PostgreSQL statements, locking behavior, driver encoding, constraint mappings, and end-to-end persisted results therefore remain unverified for Phase 2. Tests using fake backends are not evidence that those database behaviors pass. Ambiguous network/commit-outcome recovery also needs live verification.

## Reproduce local checks without a database

```sh
GOCACHE="$PWD/.cache/go-build" GOMODCACHE="$PWD/.cache/go-mod" \
go test -race ./internal/access ./internal/memory ./internal/jsonutil ./internal/httpapi ./internal/config ./cmd/...
GOCACHE="$PWD/.cache/go-build" GOMODCACHE="$PWD/.cache/go-mod" \
go test ./internal/database -run '^Test(BundledMigrationSequence|MemoryRecordPreservesRevisionsAndNormalizesTime|MemoryStorageErrorsAreSanitized)$'
make vet
make build
```

`make test-db` runs the opt-in database suites and must only be run against the Docker database when the user explicitly requests that verification. The phase tracker remains in progress until the required database-backed walkthrough and checks pass.

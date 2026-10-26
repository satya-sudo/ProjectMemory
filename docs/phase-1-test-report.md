# Phase 1 test report

Date: 2026-10-25. Scope: existing Go foundation and local PostgreSQL before Phase 2.

## Result

The expanded automated Go suite passed with the race detector enabled: 30 named tests/subtests, no failures, no skips. Go vet and the executable build passed. No production-code changes were needed for the tested behaviors.

Database checks ran against PostgreSQL 18.6 in the project's Docker Compose container. Each database test uses a generated isolated schema and cleans it up. Tests do not modify existing project records or the public migration history.

## Verified behaviors

| Area | Evidence |
| --- | --- |
| Configuration | Rejects non-loopback/invalid addresses, invalid ports, and missing database URL; accepts local IPv4/IPv6 |
| Migration replay | Repeated migration calls apply one initial schema version |
| Concurrent setup | Eight simultaneous migration/bootstrap calls return the same profile/client identities, with one profile, two clients, and one schema version |
| Failed migration | A conflicting table causes rollback without leaving migration tracking or partial project tables; migration succeeds after the conflict is removed |
| Migration compatibility | Altered checksums, newer versions, and missing-history gaps are rejected; readiness fails |
| Bootstrap lifecycle | Repeat calls preserve IDs; revoked clients are replaced without replacing unaffected identities or deleting revoked records |
| Transaction failures | Callback errors, panics, and cancellation do not commit partial writes; the pool remains usable |
| Read snapshots | Queries within a repeatable-read transaction see the same state despite a concurrent committed write; writes in the snapshot transaction are rejected |
| Domain constraints | Rejects blank titles, negative revisions, invalid document states, accepted decisions without evidence, unresolved resolutions, and verified progress without evidence |
| Relational integrity | Rejects missing parents and deletion of referenced projects |
| Uniqueness | Rejects a second overview, duplicate checkpoint revision, duplicate workspace binding, and duplicate client/idempotency key |
| History/evidence structure | Rejects missing required snapshots, non-object audit metadata, and non-array progress evidence |
| HTTP liveness | Does not consult or wait for the database |
| HTTP readiness | Returns 200 when ready and 503 on failure; slow database checks are bounded by the handler's deadline |
| HTTP output | Internal database error text is not returned; unexpected Host headers are rejected |
| Live restart/recovery | See below |

## Live recovery verification

`make verify-local` starts a temporary Go process and creates a temporary stored marker. It then:

1. Verifies health 200 and readiness 200.
2. Stops this project's database container; verifies health 200 and readiness 503.
3. Recreates the container using the existing volume; verifies readiness recovers to 200.
4. Confirms the stored marker and local profile/client IDs survived.
5. Restarts the Go backend; confirms readiness and stored data again.
6. Stops the temporary backend and removes the temporary marker schema.

This check intentionally interrupts only the project development database. It does not delete its volume. PostgreSQL is left running after the check.

## Coverage and limits

Measured statement coverage from the race-enabled Go suite:

| Package | Coverage |
| --- | --- |
| Configuration | 87.5% |
| Database | 79.2% |
| HTTP handlers | 100.0% |
| CLI entry point | 0.0% in the Go coverage run |
| Overall | 64.6% |

The separate executable restart/recovery check exercises the CLI and server startup, but its coverage is not included in the Go test coverage numbers. Coverage is a description of execution, not proof of correctness.

Project CRUD, checkpoint API conflict/replay behavior, authentication, FastMCP integration, and React flows are not implemented yet and were not tested. The idempotency test here verifies the database uniqueness constraint only, not the future API replay semantics. Backup/restore and a fully offline product walkthrough remain later-phase work.

## Reproduce

With `.env` present in the project root (automatically loaded by the development commands):

```sh
make db-up
make test-db
make test
make vet
make build
make verify-local
```

To include database tests and coverage in one direct Go run, explicitly export the connection settings first; direct `go test` does not invoke the dotenv development launcher:

```sh
PROJECTMAP_TEST_DATABASE_URL="$PROJECTMAP_DATABASE_URL" \
GOCACHE="$PWD/.cache/go-build" GOMODCACHE="$PWD/.cache/go-mod" \
go test -race -count=1 -coverprofile=.local/phase1-coverage.out ./...
```

Test reports/build outputs in `.local` are ignored by Git.

## Configuration follow-up — 2026-10-26

Automatic `.env` loading was added after the baseline report above. Seven additional configuration tests cover file loading and `os.Getenv` visibility, environment overrides, explicitly empty overrides, optional missing files, required values, malformed files without partial loading/credential exposure, and safe file-read failures. The existing 30-test baseline and its coverage figures above describe the earlier run, not a coverage measurement of this follow-up.

`make test-db` now resolves its optional test URL through the shared dotenv loader and falls back to the development database only for that explicit command. Ordinary `make test` retains opt-in integration-test behavior. Runtime and development setup no longer require shell-sourcing `.env`.

Follow-up verification passed: configuration tests, race-enabled local tests, vet, build, and dotenv-only database/migrate/bootstrap/recovery commands. The user subsequently requested that future Docker commands and Docker-backed tests run only with explicit authorization; follow that rule for subsequent work.

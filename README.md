# ProjectMap

Local project-idea memory for coding agents and an idea board dashboard.

Current implementation: Phase 1 foundation is complete. Phase 2 API code now includes local access, project capture/search, document and decision updates, atomic checkpoints, context retrieval, workspace binding, and history. Local rules and HTTP tests pass using fake backend/identity implementations. New PostgreSQL identity/project integration tests are written but have not been run; Phase 2 remains in progress until live verification is explicitly requested and passes. FastMCP and the React dashboard follow in Phases 3 and 4.

## Local setup with Docker

Requirements: Go 1.25 or newer and Docker with Compose, with its engine running. Go runs directly on the Mac; Compose runs only PostgreSQL 18. The Makefile supports Docker Desktop's bundled CLI/helper path when Docker is absent from the shell's PATH.

```sh
cp .env.example .env
make db-up
make migrate
make bootstrap
make run
```

PostgreSQL listens only on `127.0.0.1:15432`; the backend defaults to `127.0.0.1:8080`. The Compose database/user are `projectmap`; the sample password is a local development default. If changing it before first initialization, set `PROJECTMAP_POSTGRES_PASSWORD` and update `PROJECTMAP_DATABASE_URL` consistently. Changing the Compose environment after initialization does not change an existing database role's password.

Go commands and development checks automatically load `.env` from the current working directory; run these commands from the project root. Existing process environment variables take priority, including explicitly empty values. `.env` fills missing values, then application defaults apply. A missing file is allowed when configuration is supplied externally; an unreadable or malformed file fails with a credential-safe error. Loaded values are available through `os.Getenv`. No shell sourcing is needed. `.env` is ignored by Git; only `.env.example` is committed.

Compose also reads `.env` and creates the development database and role on first initialization. It keeps data in the `projectmap_postgres_data` named volume. Go does not automatically reconcile a changed database URL/password with an existing PostgreSQL role; keep the settings consistent.

```sh
make db-status
make db-stop
make db-up
```

Stopping or recreating the container without deleting its volume preserves data. Do not use `docker compose down -v` for normal shutdown; it deletes the project's stored database. PostgreSQL's container storage layout follows the [official image documentation](https://hub.docker.com/_/postgres).

## Alternative: native PostgreSQL

Install PostgreSQL through your preferred native distribution, for example [Postgres.app](https://postgresapp.com/), and make its command-line tools available in your shell. PostgreSQL is not installed automatically by this project.

Against a running local instance, use its administrator connection to create a development role and database:

```sh
createuser --pwprompt projectmap
createdb --owner=projectmap projectmap
```

Choose a password and update `.env` with your native installation's port and credentials (often port 5432, unlike Compose's 15432). Then run:

```sh
make migrate
make bootstrap
make run
```

## Backend commands

`bootstrap` returns the local profile, dashboard client, and MCP client IDs. It is safe to repeat. `migrate` is also repeatable and validates checksums of migrations already applied. Migration failures roll back the transaction. The Go commands do not create a database or start PostgreSQL.

Once the database is available and migrated, provision credentials explicitly:

```sh
make credentials
```

This command reuses/bootstrap-checks the local identities and writes two random credentials to `.local/credentials.json` with file permissions `0600`. The first entry corresponds to the dashboard client and the second to the MCP client; each includes its stable `client_id`. Credentials are not printed. Read/configure them privately from that file and never include them in project memory. `PROJECTMAP_CREDENTIALS_FILE` overrides the path. Provisioning refuses to overwrite an existing file. The default path and any file named `credentials.json` are ignored by Git. Restart the backend after provisioning or replacing the file; it loads credential digests once at startup.

If the credential file is absent, health endpoints remain usable and authenticated endpoints return 401. A malformed file or non-private file prevents server startup. The backend checks that clients still exist and are not revoked in PostgreSQL on every authenticated request. Revoking a client is currently a local administrative database action; public management/rotation APIs are outside this increment.

### Access endpoints

| Route | Behavior |
| --- | --- |
| `GET /api/v1/me` | Return the authenticated profile/client; browser sessions also receive their CSRF token |
| `POST /api/v1/session` | Exchange a dashboard credential for an HttpOnly, SameSite=Strict session cookie |
| `DELETE /api/v1/session` | Log out a browser session and clear its cookie; requires `X-CSRF-Token` |

Agents send `Authorization: Bearer <credential>` to `/api/v1/me` and future project APIs. Browser login sends JSON `{ "credential": "<dashboard credential>" }` with `Content-Type: application/json` and an `Origin` matching the configured local server origin exactly. Cookies are host-only and scoped to `/api/v1`; the local HTTP server does not set the Secure flag. Do not expose this local-only configuration as a hosted service.

Sessions live in memory, expire after 12 hours, and are invalidated by logout or backend restart. Only dashboard credentials create browser sessions. Login allows 20 attempts per minute across the local instance and at most 1,000 active sessions; excess attempts/capacity return 429 with `Retry-After`. For cookie-authenticated mutations, supply the CSRF token from `/api/v1/me`. Cross-origin requests are rejected, including bearer requests carrying a foreign Origin. An invalid Authorization header never falls back to a cookie. A known credential with an unavailable identity store returns a safe 503, without exposing database errors.

Check the running service using the configured IP and port (the Host header must match that address):

```sh
curl -i http://127.0.0.1:8080/healthz
curl -i http://127.0.0.1:8080/readyz
```

The service remains live when PostgreSQL is unavailable; readiness returns 503 until the database is reachable and its schema matches the binary. Migrations run only through the explicit command, not automatically on server startup.

## Project-memory API

All domain routes require bearer or browser-session authentication. IDs and ownership come from the authenticated client, not request-supplied profile fields.

| Route | Purpose |
| --- | --- |
| `POST /api/v1/projects` | Create a project, overview, initial proposed decisions/open questions, and optional workspace binding |
| `GET /api/v1/projects` | Search title/summary and list active, archived, or all ideas |
| `GET /api/v1/projects/{id}` | Retrieve project metadata and section counts |
| `POST /api/v1/projects/{id}/checkpoints` | Atomically change project metadata, documents, decisions, questions, progress, or bindings |
| `GET /api/v1/projects/{id}/context` | Retrieve a consistent context packet with explicit truncation and continuation information |
| `GET /api/v1/projects/{id}/documents` | List document metadata; append `/{document_id}` for full content |
| `GET /api/v1/projects/{id}/decisions` | List/filter decision records |
| `GET /api/v1/projects/{id}/questions` | List/filter open/resolved/dismissed questions |
| `GET /api/v1/projects/{id}/progress` | List progress outcomes and evidence |
| `GET /api/v1/projects/{id}/workspace-bindings` | List the project's associated local directories |
| `GET /api/v1/workspace-bindings/resolve` | Resolve an exact canonical `workspace_key` |
| `GET /api/v1/projects/{id}/history` | List checkpoints; append `/{checkpoint_id}` for full before/after snapshots |

Both write endpoints require a caller-generated `Idempotency-Key`. Checkpoints additionally require `expected_revision`, a summary, and 1–50 typed operations. Requests with a reused key and different input return 409; successful retries replay the stored result before checking the now-stale revision. A stale new write returns 409 with the current revision. Cookie-authenticated writes also require `X-CSRF-Token`.

The PostgreSQL backend rechecks the writing client, locks the project revision, and commits current records, one checkpoint, change snapshots, and retry data together. Unknown fields, duplicate/case-aliased JSON properties, unrecognized operation types, noncanonical workspace paths, and cross-project target IDs are rejected. Editing accepted content resets its acceptance state; explicitly accepting a document or decision requires a user-agreement reference. Verified progress requires evidence, and changing an outcome without replacement evidence resets it to reported.

Archive/unarchive and workspace binding are checkpoint operations, not independent write endpoints. Archived projects remain readable but require a standalone unarchive checkpoint before editing. Hard deletion and restoration are deferred. No schema migration beyond the existing initial schema is required for these endpoints.

Project-scoped list cursors are signed and bound to the profile, filters, page size, and project revision; changed projects return `cursor_stale`. Backend restarts invalidate cursors, so start a fresh listing. Context coverage includes totals, returned counts, page size, `next_cursor`, and a relative `next_url` with the correct filters/page size. Follow all accepted-decision pages before treating the set as complete. Context retrieval uses deterministic database reads and no remote model.

See [API examples](docs/phase-2-api-examples.md), [the detailed contract](docs/api-design.md), and [verification status](docs/phase-2-test-report.md).

`PROJECTMAP_ADDR` defaults to `127.0.0.1:8080` and accepts loopback IPs only. `PROJECTMAP_DATABASE_URL` is required. Database configuration and errors are not printed with connection credentials. Stop the backend with Ctrl-C. Manage PostgreSQL separately through Compose or its native installation.

## Development checks

```sh
GOCACHE="$PWD/.cache/go-build" GOMODCACHE="$PWD/.cache/go-mod" \
go test -race ./internal/access ./internal/memory ./internal/jsonutil ./internal/httpapi ./internal/config ./cmd/...
make vet
make build
```

This targeted test command does not access a database. Docker commands and Docker-backed checks require an explicit user request; do not infer that permission from a general implementation/testing request. `make test` remains available for the entire suite, but it may run integration tests if `PROJECTMAP_TEST_DATABASE_URL` is configured.

The executable is `.local/projectmap`. Go build/module caches stay under the ignored `.cache` directory. Initial dependency downloads require a network connection; ordinary local runtime does not require network access beyond the local database.

Real database checks are opt-in:

```sh
make test-db
```

`make test-db` uses `PROJECTMAP_TEST_DATABASE_URL` when set; otherwise it uses the validated `PROJECTMAP_DATABASE_URL` from the environment or `.env`. An explicitly empty test URL is an error. The test connection must be allowed to create/drop schemas. Tests use a fresh generated schema and remove only that schema; they do not migrate the database's public schema. They cover foundation behavior plus the new opt-in identity and project-memory suites: create/replay, checkpoints, rollback, ownership, context pagination, coalesced audit entries, archive/unarchive, literal search, concurrent retries, and revision conflicts. The new Phase 2 database suites have not yet been executed. Run Docker-backed checks only when explicitly requested.

Ordinary `make test` loads `.env` but does not fall back to the development database: database tests run only when a nonempty `PROJECTMAP_TEST_DATABASE_URL` is explicitly configured. Direct `go test` bypasses the development launcher, so supply environment variables yourself when running database tests that way. The launcher executes child commands directly; it never sources `.env` as shell code or prints configuration values.

For the Compose development database, `make verify-local` checks the complete startup/outage/recovery flow. It intentionally stops and recreates this project's database container without deleting its volume, so run it when no other project work needs that database. It creates and removes a uniquely named verification schema, checks that a stored marker and local identities survive container recreation, and checks a Go backend restart. It leaves PostgreSQL running and stops the temporary Go process.

## Storage and migrations

Data lives in Compose's named volume (or a native PostgreSQL data directory), not in this application's cache or executable. Restarting the Go process does not clear database data. Backup/restore validation belongs to Phase 5.

Migrations are embedded from `internal/database/migrations` and applied under a transaction-scoped lock. Add sequential forward migrations; never edit an applied migration. Down migrations and automatic destructive resets are intentionally absent.

## Planning documents

- [Product flows and architecture](docs/product-and-architecture.md)
- [Implementation phases and current verification](docs/implementation-phases.md)
- [Database schema](docs/database-schema.md)
- [API and MCP contracts](docs/api-design.md)

The driver uses [pgx](https://github.com/jackc/pgx); the selected dependency versions are locked in `go.mod` and `go.sum`.

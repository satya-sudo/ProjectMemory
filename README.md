# ProjectMap

Local project-idea memory for coding agents and an idea board dashboard.

Current implementation: Phase 1 Go backend foundation is complete and verified against PostgreSQL 18.6 in Docker. Project APIs, credentials/sessions, FastMCP, and the React dashboard are not implemented yet.

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

`bootstrap` returns the local profile, dashboard client, and MCP client IDs. It is safe to repeat and does not issue authentication credentials yet. `migrate` is also repeatable and validates checksums of migrations already applied. Migration failures roll back the transaction. The Go commands do not create a database or start PostgreSQL.

Check the running service using the configured IP and port (the Host header must match that address):

```sh
curl -i http://127.0.0.1:8080/healthz
curl -i http://127.0.0.1:8080/readyz
```

The service remains live when PostgreSQL is unavailable; readiness returns 503 until the database is reachable and its schema matches the binary. Migrations run only through the explicit command, not automatically on server startup. Only health/readiness routes exist at this point.

`PROJECTMAP_ADDR` defaults to `127.0.0.1:8080` and accepts loopback IPs only. `PROJECTMAP_DATABASE_URL` is required. Database configuration and errors are not printed with connection credentials. Stop the backend with Ctrl-C. Manage PostgreSQL separately through Compose or its native installation.

## Development checks

```sh
make test
make vet
make build
```

The executable is `.local/projectmap`. Go build/module caches stay under the ignored `.cache` directory. Initial dependency downloads require a network connection; ordinary local runtime does not require network access beyond the local database.

Real database checks are opt-in:

```sh
make test-db
```

`make test-db` uses `PROJECTMAP_TEST_DATABASE_URL` when set; otherwise it uses the validated `PROJECTMAP_DATABASE_URL` from the environment or `.env`. An explicitly empty test URL is an error. The test connection must be allowed to create/drop schemas. Tests use a fresh generated schema and remove only that schema; they do not migrate the database's public schema. They cover migration replay/checksums, bootstrap replay, transaction rollback, consistent read snapshots, read-only enforcement, schema constraints, readiness, and data visibility through a new connection.

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

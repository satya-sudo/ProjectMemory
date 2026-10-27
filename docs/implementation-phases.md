# MVP implementation phases

This file is the implementation tracker. These phases describe building this application; they do not introduce Kanban stages into the product.

Status values: not started, in progress, blocked, complete. Mark a phase complete only when its exit criteria have evidence. Record blockers and evidence here as work progresses.

## Phase tracker

| Phase | Outcome | Depends on | Status |
| --- | --- | --- | --- |
| 0 — Product and contracts | Reviewed flows, schema, and API/tool contracts | — | In progress |
| 1 — Local foundation and storage | Go backend and durable local PostgreSQL | 0 | Complete |
| 2 — Project memory API | Capture, retrieve, update, and resume through Go | 1 | In progress |
| 3 — Agent integration | FastMCP capture/resume loop in a coding agent | 2 | Not started |
| 4 — Idea board | React board and editable project workspace | 2; release validation also needs 3 | Not started |
| 5 — Local MVP release | Reliable offline operation, installation, and recovery | 3, 4 | Not started |

## Phase 0 — Product and contracts

- [x] Describe user flow, agent flow, and component responsibilities.
- [x] Select local/offline operation, FastMCP, Go, React, and PostgreSQL.
- [x] Draft implementation phases and database design.
- [x] Review schema baseline: user accepted it as sufficient for the MVP, with refinements during implementation.
- [x] Draft HTTP request/response shapes, errors, and matching MCP tool contracts in `api-design.md`.
- [ ] Review API contract, acceptance rules, and the proposed change-item audit metadata refinement.
- [ ] Choose FastMCP package/language, transport, and first agent integration.
- [x] Choose Docker Compose PostgreSQL for development after the user installed Docker; native PostgreSQL remains an alternative.
- [ ] Choose frontend build tooling before Phase 4.

Exit: enough agreed contracts to implement storage and API without inventing product behavior. Draft artifacts are not user approval.

## Phase 1 — Local foundation and storage

- [x] Create Go application structure, configuration, and health/readiness endpoints.
- [x] Add embedded versioned PostgreSQL migration and explicit migration command; verified against PostgreSQL 18.6.
- [x] Implement repeatable local profile/client identity bootstrap; live verification passed, credentials remain Phase 2.
- [x] Add pool/store and write/read-snapshot transaction helpers; domain repositories follow with Phase 2.
- [x] Document Docker and native database setup, commands, and storage ownership in README.
- [x] Run live PostgreSQL migrations, bootstrap, constraint checks, and process/database restart persistence checks.

Exit: a clean database migrates successfully; restarting preserves sample data; readiness reports database failures accurately. Schema constraints are exercised against PostgreSQL.

## Phase 2 — Project memory API

- [x] Implement Phase 2.1 local credential provisioning, bearer authentication, browser session login/logout, CSRF, origin checks, throttling, and `/api/v1/me` with local tests.
- [ ] Verify new identity resolution and credential provisioning against PostgreSQL when explicitly authorized. The opt-in isolated-schema test is written but has not been run.
- [x] Implement project creation, listing/search, detail, metadata updates, and archiving (SQL live verification pending).
- [x] Implement document, decision, question, and progress operations with database-independent validation/state-rule tests.
- [x] Implement workspace bindings and compact context retrieval with signed revision-bound continuation cursors (SQL live verification pending).
- [x] Implement atomic checkpoint writes, revision conflicts, provenance, and idempotency (SQL live verification pending).
- [x] Implement history retrieval and project ownership enforcement in domain operations (SQL live verification pending).
- [x] Add authenticated HTTP routing, strict JSON/query validation, replay headers, CSRF checks, safe errors, and local tests with fake backends.
- [ ] Run the new opt-in PostgreSQL identity and memory integration tests when explicitly authorized; verify rollback/replay/concurrency/context/history against the real database.

Build order: 2.1 local access → 2.2 project capture → 2.3 atomic memory updates → 2.4 resume/context → 2.5 API reliability verification. Phase 2 is not complete until the project-memory walkthrough and required checks pass.

Exit: API-only walkthrough captures an idea, adds plans, records a decision, and resumes correct context. Concurrent writes cannot silently overwrite each other; retries cannot duplicate writes; failed transactions leave no partial history.

## Phase 3 — Agent integration

- [ ] Implement FastMCP tools as thin clients of the Go API.
- [ ] Add integration instructions for explicit capture and meaningful checkpoints.
- [ ] Add configuration and setup instructions for the first agent.
- [ ] Handle ambiguous project lookup, API unavailable, and revision conflicts.
- [ ] Prove capture and fresh-session resume, with dashboard links in tool results.

Exit: “track this project” creates exactly one project; a fresh agent session retrieves its current plans and decisions. A failed save is visibly reported. Core behavior uses service-side validation rather than relying only on agent instructions.

## Phase 4 — Idea board and workspace

- [ ] Build the project card board with search and archived-project access.
- [ ] Build overview, plans, architecture, decisions, questions, and progress views.
- [ ] Add Markdown/text editing, draft/accepted presentation, and history browsing.
- [ ] Handle stale edits and save failures without losing the user's draft.
- [ ] Serve the production frontend locally through Go with bundled assets.

Exit: a user can browse, edit, and archive ideas; subsequent agent reads include dashboard edits. No mandatory workflow stages appear. The production UI operates without external assets.

## Phase 5 — Local MVP release

- [ ] Validate the second coding agent against the same project memory.
- [ ] Provide a documented start/stop/setup flow for all local components.
- [ ] Provide PostgreSQL backup and restore instructions and verify a restore.
- [ ] Verify offline dashboard and direct MCP operations after installation.
- [ ] Exercise restarts, API/DB outages, concurrent clients, and duplicate requests.
- [ ] Document limitations and version/migration compatibility.

Exit: a clean setup can capture an idea, refine it, edit through the dashboard, resume with another agent, restart, and recover from backup. Offline verification separates local service behavior from the agent model's network requirements.

## Current evidence and blockers

- Design artifacts: `product-and-architecture.md`, `database-schema.md`, `api-design.md`, and this tracker.
- Go application, embedded initial schema migration, bootstrap, configuration, pool/transaction helpers, and health/readiness are implemented.
- Verification on 2026-10-25: race-enabled unit tests, Go vet, and binary build pass.
- Live PostgreSQL 18.6 integration tests passed: migration/bootstrap replay, checksum rejection, transaction rollback, repeatable-read consistency, read-only enforcement, required-state constraints, overview uniqueness, and data visibility through new connections. Run with `PROJECTMAP_TEST_DATABASE_URL` and `make test-db`.
- `make verify-local` passed: health 200 and readiness 200 with PostgreSQL available; health 200/readiness 503 during a database outage; readiness recovered to 200 after container recreation. Stored marker data and bootstrap IDs survived the preserved Docker volume, and a new Go backend process read the same data.
- Historical Phase 1 verification found a healthy Docker database on `127.0.0.1:15432`, named volume `projectmap_postgres_data`, one profile, two clients, and no leftover verification schemas. This describes the Phase 1 check, not a current Docker-state inspection.
- Phase 1 is complete. Docker Desktop's bundled CLI and credential-helper path are supported in Makefile commands. No native PostgreSQL installation was needed.
- Additional pre-Phase-2 testing: 30 named tests/subtests passed with race detection, including concurrent setup, interrupted transaction rollback, failed migration cleanup, revoked-client replacement, schema integrity edges, and HTTP readiness timeouts. Details and coverage are in [Phase 1 test report](phase-1-test-report.md).
- Configuration follow-up: Go runtime and development commands automatically load optional `.env` through godotenv, preserving existing variables (including explicit empty values). Seven dotenv tests verify precedence, required settings, missing/malformed/unreadable files, `os.Getenv` availability, and credential-safe errors. Local `.env` stays Git-ignored; no schema changes are needed.
- Phase 2 API implementation is in place, including access controls, project capture, checkpoint operations, context, workspace binding, and history. Local rule/HTTP tests run without a database. New PostgreSQL integration tests are opt-in and remain unexecuted, so Phase 2 is not marked complete. No Docker commands or live database checks were run during this implementation. See [Phase 2 test report](phase-2-test-report.md). FastMCP and dashboard features remain future phases.
- Phase 2 local verification: 104 named tests/subtests passed with race detection, plus 3 static database-package tests. Vet, build, and whitespace checks passed. Integration tests were compiled only, not executed.

Deferred: automatic idea capture, hosted/hybrid deployments, multi-user collaboration, cloud synchronization, vector search, and a task dependency engine.

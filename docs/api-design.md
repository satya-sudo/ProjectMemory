# HTTP API and MCP contracts — MVP

Status: Phase 2 API code is implemented with local rules/HTTP tests. New PostgreSQL integration tests are written but not run; live verification remains pending. Uses the accepted MVP schema baseline in [database-schema.md](database-schema.md). The FastMCP adapter will expose this Go HTTP contract.

## Contract decisions

- Base path: `/api/v1`. JSON uses snake_case. IDs are UUID strings; timestamps are UTC RFC 3339 strings.
- One local profile is inferred from trusted client configuration. The caller cannot choose its owner or impersonate another client.
- Dashboard and FastMCP use identical domain endpoints. All business validation stays in Go.
- Two domain write endpoints: create a project, or apply an atomic checkpoint to an existing project. A dashboard editing one document sends a one-operation checkpoint.
- No independent document/decision write endpoints in the MVP. This keeps provenance, revisions, and retries consistent.
- Reads never update project activity or create history. Listing/search does not automatically bind a workspace or select a project.
- HTTP is local to the Go service. The MCP transport between the agent and FastMCP is a separate setup decision.

## Endpoint inventory

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/healthz` | Process alive; outside versioned API |
| GET | `/readyz` | Database reachable and migrations compatible |
| GET | `/api/v1/me` | Configured profile and current client |
| GET | `/api/v1/projects` | Board listing and title/summary search |
| POST | `/api/v1/projects` | Explicit capture with initial memory |
| GET | `/api/v1/workspace-bindings/resolve` | Exact workspace lookup |
| GET | `/api/v1/projects/{project_id}` | Project metadata and section counts |
| GET | `/api/v1/projects/{project_id}/context` | Compact, consistent agent context |
| GET | `/api/v1/projects/{project_id}/documents` | Document metadata list |
| GET | `/api/v1/projects/{project_id}/documents/{document_id}` | Full current document |
| GET | `/api/v1/projects/{project_id}/decisions` | Decisions, optionally filtered by state |
| GET | `/api/v1/projects/{project_id}/questions` | Questions, optionally filtered by state |
| GET | `/api/v1/projects/{project_id}/progress` | Recent progress and evidence |
| GET | `/api/v1/projects/{project_id}/workspace-bindings` | Project's local workspace associations |
| GET | `/api/v1/projects/{project_id}/history` | Checkpoint timeline |
| GET | `/api/v1/projects/{project_id}/history/{checkpoint_id}` | Checkpoint and full change items |
| POST | `/api/v1/projects/{project_id}/checkpoints` | Atomic project/content/binding updates |

No public profile/client provisioning, hard-delete, history-edit, or restore endpoint in this MVP. Provisioning is a local setup operation. Archive/unarchive are checkpoint operations.

## Local access contract

Proposed MVP access mechanism:

- Bind Go to loopback by default. Validate Host and browser Origin against configured local addresses; disable cross-origin API access in production.
- Provision one random bearer credential per configured client outside the domain tables. FastMCP sends it in `Authorization: Bearer ...`; Go resolves it to a non-revoked client and profile.
- The dashboard uses a distinct local session credential in an HttpOnly, SameSite=Strict cookie. An explicit local setup/login flow exchanges a dashboard credential for this session through `POST /api/v1/session`; `DELETE /api/v1/session` clears it. These are authentication endpoints, not project mutations, and do not use checkpoint/idempotency rules.
- The browser supplies a per-session CSRF token on mutations in `X-CSRF-Token`; `GET /api/v1/me` returns that token for authenticated browser sessions only. Validate same origin on session creation as well. Do not put credentials in project documents, URLs, application logs, or browser localStorage.
- Bearer requests do not need CSRF tokens. Browser-origin requests must still pass the origin policy. Require credentials for domain reads and writes; health/readiness expose no project data or configuration secrets.

Session response: `{ "data": { "authenticated": true } }`; login body: `{ "credential": "..." }`. Wrong credentials return 401; logout returns 204. Sessions are in memory with absolute 12-hour expiry and are lost on restart. They are capped at 1,000 active sessions. Login is limited to 20 attempts per minute across the local instance; excess attempts/capacity return 429 with `Retry-After: 60`. Login JSON is capped at 16 KiB; credential strings at 128 bytes. These routes do not introduce remote accounts or OAuth.

`make credentials` provisions a private version-1 credential JSON file at `.local/credentials.json` (override with `PROJECTMAP_CREDENTIALS_FILE`). It stores dashboard and agent credentials outside domain tables, refuses overwrite, and does not print secrets. The server loads credential digests at startup and resolves client/profile identity and revocation from PostgreSQL on each authenticated request. Missing files leave authentication disabled; invalid/private-permission failures stop startup. This increment's live identity-query/provisioning verification is pending explicit authorization to use the local database.

## Common representations

Successful single-resource reads use `{ "data": ... }`. Lists use:

```json
{
  "data": [],
  "page": { "next_cursor": null, "has_more": false },
  "meta": { "project_revision": 4 }
}
```

`meta.project_revision` appears on project-scoped reads. List reads use `limit` (default 20, maximum 100) and an opaque `cursor`. Filters are bound into the cursor; changing them requires a fresh listing. Reject invalid/mismatched cursors with 400. Project lists sort by `updated_at DESC, id DESC`; progress/documents use their documented timestamp plus ID; history sorts by revision descending. Decisions, questions, and bindings use `created_at DESC, id DESC`.

Project-scoped pagination is tied to a project revision. A cursor from an older project revision returns 409 `cursor_stale`; restart the listing to avoid mixing versions. The board listing is best-effort across writes; refreshing restarts it. `next_cursor` is null when no further results exist. Empty collections are arrays, not null.

Implemented cursor details: cursors are signed with an in-memory key and bound to the authenticated profile, resource, filters, and page size. Changing page size requires a fresh listing. Backend restarts invalidate cursors (400 `invalid_cursor`). Context coverage additionally returns `limit` and a relative `next_url` carrying the correct filters/page size; this matters for the progress window's page size of 5.

Project representation:

```json
{
  "id": "11111111-1111-4111-8111-111111111111",
  "title": "Project Idea Memory",
  "summary": "Persistent project ideas shared with coding agents.",
  "next_step": "Define the API contracts",
  "revision": 4,
  "archived_at": null,
  "created_at": "2026-10-25T12:00:00Z",
  "updated_at": "2026-10-25T12:30:00Z",
  "dashboard_url": "http://localhost:PORT/projects/11111111-1111-4111-8111-111111111111"
}
```

The configured runtime supplies the real dashboard origin; `PORT` above is illustrative. Project detail also returns section counts. Do not include complete documents in board cards.

Content resources return their schema fields, IDs, project ID, timestamps, and `last_changed_revision`. Workspace bindings return their schema fields. Document lists omit `content`; full document reads include it. History lists omit before/after snapshots; checkpoint detail includes them. All nested IDs must belong to the project in the URL; otherwise return 404.

`GET /me` returns `{data: {profile: {id, display_name}, client: {id, name, kind}, csrf_token: "..."}}`; omit `csrf_token` for bearer clients.

## Create a tracked project

`POST /api/v1/projects` requires `Idempotency-Key`. It creates the project, its overview, optional initial proposed questions/decisions and workspace binding, and checkpoint revision 1 in one transaction.

```json
{
  "title": "Project Idea Memory",
  "summary": "Keep project ideas and plans available across agent sessions.",
  "next_step": "Refine feature scope",
  "overview": {
    "title": "The idea",
    "content": "## Problem\nIdeas and decisions get lost between sessions."
  },
  "questions": [
    { "question": "Which coding agent should we integrate first?" }
  ],
  "decisions": [
    { "title": "Deployment", "statement": "Start with a local MVP", "rationale": "Keep setup independent of a hosted service." }
  ],
  "workspace": {
    "workspace_key": "/Users/example/projectMap",
    "repository_url": null
  },
  "checkpoint": {
    "summary": "Capture the explicitly tracked project idea",
    "source_reference": "conversation:session-id/message-id"
  }
}
```

Required: `title`, `overview` with title/content, and `checkpoint.summary`. Optional `summary` and `next_step` default empty; arrays default empty; omitted `workspace` means no binding. Initial documents are draft, decisions proposed, and questions open. Existing user-agreed decisions can be accepted in a later explicit checkpoint.

Return 201 with `{data: {project, checkpoint, created: {overview_document_id, decision_ids, question_ids, workspace_binding_id}}}`. Absent workspace ID is null. A claimed workspace already bound anywhere returns 409; the entire creation rolls back. Similar titles return no automatic duplicate error: agents search before creating, while request retries use idempotency.

The service cannot infer consent from conversation text. Explicit capture is an integration behavior, not an extra boolean pretending to prove user authorization.

## Browse, resolve, and retrieve

- `GET /projects?q=memory&archived=false&limit=20`: case-insensitive literal substring match on title/summary. `archived` accepts `false` (default), `true`, or `all`. Escape SQL wildcard characters so `%`/`_` in input are literal. Do not silently choose the first match.
- `GET /workspace-bindings/resolve?workspace_key=...`: the adapter supplies the canonical absolute directory path as an encoded query parameter. Return `{data: {binding, project}}` or 404 `workspace_unbound`. Archived projects still resolve and carry their archived state. Do not use repository URL alone as identity.
- Document list: `kind` and `state` filters, `updated_at DESC, id DESC` order.
- Decisions/questions: optional exact `state` filter using the respective schema vocabulary; all states by default.
- Progress: `created_at DESC, id DESC`; includes evidence.
- History uses the shared cursor scheme; no separate `before_revision` parameter. Checkpoint detail includes the writer's display metadata, origin, summary, revision, and ordered `change_items`.

## Context packet

`GET /projects/{project_id}/context` returns one consistent snapshot. Proposed fixed default limits keep the first implementation predictable: overview preview 8,000 UTF-8 bytes, 20 decisions per state, 20 open questions, 20 document references, and 5 progress entries. These are page limits, not a promise to fit an arbitrary model's token budget.

```json
{
  "data": {
    "project": { "id": "11111111-1111-4111-8111-111111111111", "revision": 4 },
    "overview": {
      "document_id": "22222222-2222-4222-8222-222222222222",
      "state": "draft",
      "preview": "## Problem\nIdeas get lost...",
      "truncated": false
    },
    "documents": [],
    "accepted_decisions": [],
    "proposed_decisions": [],
    "open_questions": [],
    "recent_progress": [],
    "coverage": {
      "documents": { "total": 0, "returned": 0, "next_cursor": null },
      "accepted_decisions": { "total": 0, "returned": 0, "next_cursor": null },
      "proposed_decisions": { "total": 0, "returned": 0, "next_cursor": null },
      "open_questions": { "total": 0, "returned": 0, "next_cursor": null },
      "recent_progress": { "total": 0, "returned": 0, "next_cursor": null }
    }
  }
}
```

Example project is abbreviated; actual response includes the full project representation and its next step. Overview is null if absent in imported or incomplete data. Document references include ID, title, kind, state, and revision, not full bodies; include the overview in the reference listing too. Coverage totals and cursors describe the same snapshot and point to the corresponding list endpoints with the applicable state filter. Never silently omit accepted decisions: agents must follow continuation cursors before treating a decision set as complete. Overview truncation ends on a valid Unicode boundary. Full document bodies are fetched separately.

Context assembly is deterministic retrieval; no remote model or summarization service is required. The adapter fetches full documents as needed. If the revision changes between context and document reads, it reloads context before writing based on mixed snapshots.

## Apply a checkpoint

`POST /projects/{project_id}/checkpoints` requires `Idempotency-Key` and:

```json
{
  "expected_revision": 4,
  "summary": "Add the API design and record the next step",
  "source_reference": "conversation:session-id/message-id",
  "operations": [
    {
      "ref": "api-design",
      "type": "document.create",
      "data": {
        "kind": "architecture",
        "title": "API design",
        "content": "## Endpoints\n..."
      }
    },
    {
      "ref": "next-action",
      "type": "project.update",
      "data": { "next_step": "Implement the Go foundation" }
    }
  ]
}
```

`expected_revision` is a positive integer. `summary` is nonblank. `source_reference` is optional. `operations` is a nonempty ordered array. Each operation has a unique request-local `ref`, a fixed `type`, and the fields below. Creates take no ID; Go generates it. Existing targets require `id`. Fields that are omitted in updates stay unchanged; explicit null clears only nullable fields. Unknown fields and server-owned fields are rejected. Updates require at least one editable field.

| Operation type | Target | Allowed `data` fields / semantics |
| --- | --- | --- |
| `project.update` | Project in path; no ID | `title`, `summary`, `next_step` |
| `project.archive` | Project in path; no ID | No data; sets archive timestamp |
| `project.unarchive` | Project in path; no ID | No data; clears archive timestamp |
| `document.create` | New document | Required `kind`, `title`, `content`; state defaults draft |
| `document.update` | Existing document ID | `title`, `content`; kind is immutable; content/title edits reset accepted state to draft |
| `document.set_state` | Existing document ID | Required `state`; optional `acceptance_reference`, required and nonblank for acceptance |
| `decision.create` | New decision | Required `title`, `statement`; optional `rationale`, `source_reference`; state defaults proposed |
| `decision.update` | Existing decision ID | `title`, `statement`, `rationale`, `source_reference`; resets accepted state to proposed and clears acceptance reference |
| `decision.set_state` | Existing decision ID | Required `state`; optional `acceptance_reference`, required and nonblank for acceptance |
| `question.create` | New question | Required `question`; state defaults open |
| `question.update` | Existing question ID | `question`, `state`, `resolution`; final state/resolution must satisfy schema rules |
| `progress.create` | New entry | Required `summary`; optional `verification` (default reported), `evidence` (default empty) |
| `progress.update` | Existing entry ID | `summary`, `verification`, `evidence`; changing summary without replacement evidence resets verification to reported and clears evidence |
| `workspace.bind` | New binding | Required `workspace_key`; optional `repository_url` |
| `workspace.unbind` | Existing binding ID | No data; removes binding and records prior snapshot |

For `document.set_state`, the acceptance reference is recorded in checkpoint change-item audit metadata, not as a new document field; see the schema refinement below. Decisions store their current acceptance reference on the decision as well. Client identities and references are evidence attribution, not a guarantee of user agreement.

State rules: draft documents can be accepted or superseded; accepted documents can become draft or superseded. Superseded documents are read-only in the MVP. Proposed decisions can be accepted/rejected/superseded; accepted decisions can return to proposed or become superseded. Rejected and superseded decisions are read-only. Question text can change only while its final state is open; resolved/dismissed questions may be reopened by setting `state: open, resolution: null`. This preserves a clear history of changes to resolved questions.

Reject duplicate operations against the same existing entity, except one content update followed by one state-setting operation for that document/decision. Consolidate this pair into one history item; it allows an explicitly accepted new edit without inheriting old acceptance. The same exception permits revising then superseding a record. New resources cannot be referenced by other operations in this MVP request; create first, then use returned IDs in another checkpoint when needed. No-op operations return 422 and the whole batch rolls back.

An archived project permits only an unarchive checkpoint; other mutations return 409 `project_archived`. Archive/unarchive must be the sole operation. On an active project, archive succeeds; unarchive is a 422 no-op. Reads/history remain available when archived.

Response: 201 after commit:

```json
{
  "data": {
    "project": { "id": "11111111-1111-4111-8111-111111111111", "revision": 5 },
    "checkpoint": { "id": "33333333-3333-4333-8333-333333333333", "revision": 5 },
    "results": [
      { "ref": "api-design", "entity_type": "document", "id": "44444444-4444-4444-8444-444444444444" },
      { "ref": "next-action", "entity_type": "project", "id": "11111111-1111-4111-8111-111111111111" }
    ]
  }
}
```

The actual response contains full project and checkpoint metadata, without full document bodies. Every operation either commits together at one new revision or none commits. Omit cross-project operations and arbitrary JSON Patch/database mutation instructions.

## Validation and limits

Proposed initial limits, centrally configured and identical for HTTP and MCP:

| Input | Limit |
| --- | --- |
| Whole JSON body | 2 MiB |
| Operations per checkpoint | 50 |
| Initial questions / decisions | 20 each |
| Titles, names, operation refs | 200 / 200 / 64 Unicode characters |
| Project summary / next step | 2,000 characters each |
| Document content | 256 KiB UTF-8 |
| Question / decision statement / rationale / resolution / progress summary | 8,000 characters each |
| Checkpoint summary | 2,000 characters |
| Workspace key / repository URL / source or acceptance reference | 4,096 characters |
| Evidence | At most 20 objects; nonblank `kind` (64 chars), `reference` (4,096), optional `description` (2,000) |
| Idempotency key | 1–128 ASCII characters; UUID recommended |
| Search query | 200 characters |

Malformed JSON, duplicate JSON object keys, case-aliased/unknown properties, invalid UUIDs, invalid non-nullable field types, and invalid query shapes return 400. Domain validation such as blank text, invalid transitions, missing acceptance evidence, or exceeding a field/operation limit returns 422. Oversized whole bodies return 413; unsupported content types return 415. Printable non-whitespace ASCII idempotency keys are required. All limits are enforced server-side.

## Retry and conflict rules

- The client supplies a fresh idempotency key for each intentional mutation and reuses it unchanged when retrying an uncertain outcome. MCP exposes this as a required argument and forwards it as the header.
- Replaying the same client/key and canonical request returns the original status/body with `Idempotency-Replayed: true`, even if the project has advanced since that save. The response revision is the saved operation's revision, not necessarily current.
- Reusing the key for a different operation, target, or payload returns 409 `idempotency_key_reused`.
- Idempotency lookup precedes revision comparison. A stale new write returns 409 `revision_conflict` with `expected_revision` and `current_revision`.
- Reconcile conflicting content, then submit the revised payload and expected revision with a new key. Never automatically retry stale content with the newest revision.
- For network failure/503, retry the exact request/key with bounded backoff. An uncertain commit is resolved through replay. Validation/authentication errors require correction, not blind retries.

## Errors and health

```json
{
  "error": {
    "code": "revision_conflict",
    "message": "Project changed since it was read.",
    "details": { "expected_revision": 4, "current_revision": 5 },
    "request_id": "server-generated-request-id"
  }
}
```

Validation details contain `fields: [{path, message}]`, for example `operations[0].data.title`. Never return SQL, stack traces, credentials, or raw database errors.

| Status | Stable error codes |
| --- | --- |
| 400 | `invalid_request`, `invalid_cursor` |
| 401 | `unauthorized` |
| 403 | `forbidden`, `origin_not_allowed`, `csrf_invalid` |
| 404 | `not_found`, `workspace_unbound` |
| 409 | `revision_conflict`, `cursor_stale`, `idempotency_key_reused`, `workspace_already_bound`, `project_archived` |
| 413 | `request_too_large` |
| 415 | `unsupported_media_type` |
| 422 | `validation_failed`, `invalid_transition`, `no_change` |
| 429 | `rate_limited` |
| 500 | `internal_error` |
| 503 | `service_unavailable` |

Unknown or inaccessible project IDs return 404. `/healthz` returns 200 `{status: "ok"}` when running; `/readyz` returns 200 `{status: "ready"}` or 503 `{status: "not_ready"}`. Health is separate from the domain JSON envelope.

## MCP tools

Tool names are app-owned. FastMCP tool schemas mirror the HTTP input types and reject unknown fields. Typed operations should be a discriminated union on `type`, not an unrestricted object or free-form update string. GET tool arguments omit any credential fields.

| Tool | Arguments | HTTP mapping |
| --- | --- | --- |
| `list_projects` | Optional `q`, `archived`, `limit`, `cursor` | GET projects |
| `create_project` | Required `idempotency_key`, title, overview, checkpoint; optional summary, next_step, questions, decisions, workspace | POST projects |
| `resolve_workspace` | Required `workspace_key` | GET binding resolution |
| `get_project_context` | Required `project_id` | GET context |
| `list_project_documents` | Required `project_id`; optional kind, state, limit, cursor | GET documents |
| `get_document` | Required `project_id`, `document_id` | GET full document |
| `list_project_decisions` | Required `project_id`; optional state, limit, cursor | GET decisions |
| `list_project_questions` | Required `project_id`; optional state, limit, cursor | GET questions |
| `list_project_progress` | Required `project_id`; optional limit, cursor | GET progress |
| `apply_project_update` | Required `project_id`, `idempotency_key`, expected_revision, summary, operations; optional source_reference | POST checkpoints |
| `get_project_history` | Required `project_id`; optional checkpoint_id, limit, cursor | GET history list, or detail when checkpoint_id supplied |

`get_project_history` rejects pagination arguments with `checkpoint_id`. Workspace binding is an operation of `apply_project_update`, avoiding another mutation contract/tool. Dashboard-only archiving still uses the shared backend operation; agent instructions should archive only when requested.

Successful tools return the backend data and pagination/coverage metadata as structured results, with a concise human-readable summary. Failures are surfaced as tool errors retaining the stable API code and safe details; FastMCP must not report a successful save when the API failed. No independent memory cache or database writes in FastMCP.

## Example agent sequence

1. User: “track this project.” Search projects and resolve a workspace if available.
2. If no chosen existing project, call `create_project` with one stable retry key.
3. On the next planning request, load `get_project_context` and required documents.
4. Save the feature plan and next step in one `apply_project_update` at the loaded revision.
5. Return the project link and saved outcome. Resume with the same project ID in a later session.

## Schema refinement from this API design

Add `change_items.metadata jsonb NOT NULL DEFAULT '{}'`, constrained to an object. It stores the operation's acceptance reference when a document is explicitly accepted. This closes the audit gap for document acceptance without adding an acceptance column to every document. Metadata must be validated and bounded by Go; only known fields are accepted. Do not store request credentials or full conversations here.

## Implementation contract checks

Local tests cover state rules, validation, cursor signing/staleness rules, authenticated HTTP routing, strict input rejection, CSRF, and replay/conflict response contracts using fake backends. Opt-in PostgreSQL tests cover create/replay, transaction rollback after a constraint failure, concurrent requests, stale writes, context pagination/coverage, coalesced history, literal search, archiving, ownership, and cross-project targets. Those new database tests and the new identity-resolution test have not been run; ambiguous network/commit outcomes still need live verification. Phase 2 remains in progress until the required database-backed checks pass.

Machine-readable API schemas and typed FastMCP models should follow this contract as their respective implementations are added. Do not maintain a second competing interpretation of operation shapes.

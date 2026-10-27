# Phase 2 API walkthrough

These examples describe implemented contracts. New PostgreSQL-backed flows are not yet live-verified. Replace placeholder IDs and credentials with actual values. Choose a fresh idempotency key for every new intentional write; retain the same key/body when retrying an uncertain save.

## 1. Capture an idea

```http
POST /api/v1/projects
Authorization: Bearer <agent-credential>
Content-Type: application/json
Idempotency-Key: <fresh-capture-key>
```

```json
{
  "title": "Project Memory",
  "summary": "A lasting home for project ideas and plans.",
  "next_step": "Define the MVP features",
  "overview": {
    "title": "The idea",
    "content": "## Problem\nIdeas and decisions get lost between coding sessions.\n\n## Solution\nStore each idea in one project profile."
  },
  "decisions": [
    { "title": "Deployment", "statement": "Start with a local MVP", "rationale": "Keep the first release self-contained." }
  ],
  "questions": [
    { "question": "Which coding agent should we support first?" }
  ],
  "checkpoint": { "summary": "Capture the explicitly tracked idea" }
}
```

On success, HTTP 201 returns `data.project`, `data.checkpoint`, and `data.created`. The project starts at revision 1. `created` includes the overview ID, question/decision ID arrays, and an optional workspace binding ID. The overview is draft; decisions are proposed and questions open.

## 2. Browse or resume

```http
GET /api/v1/projects?q=memory&archived=false&limit=20
Authorization: Bearer <agent-credential>
```

```http
GET /api/v1/projects/<project_id>/context
Authorization: Bearer <agent-credential>
```

Context includes project summary/next step, overview preview, document references, accepted/proposed decisions, open questions, recent progress, and coverage totals/continuation links. Fetch `/documents/<document_id>` for full text. Follow `coverage.*.next_url` when a section has more records; accepted decisions must not be treated as complete while a continuation exists.

## 3. Save a plan, agreed decision, and next step together

Use the project ID and deployment-decision ID returned by creation. The decision UUID below is illustrative.

```http
POST /api/v1/projects/<project_id>/checkpoints
Authorization: Bearer <agent-credential>
Content-Type: application/json
Idempotency-Key: <fresh-plan-key>
```

```json
{
  "expected_revision": 1,
  "summary": "Save the feature plan and agreed local deployment",
  "source_reference": "conversation:session-id/message-id",
  "operations": [
    {
      "ref": "feature-plan",
      "type": "document.create",
      "data": {
        "kind": "feature_plan",
        "title": "MVP features",
        "content": "- Explicit capture\n- Project context\n- Idea board"
      }
    },
    {
      "ref": "deployment-decision",
      "type": "decision.set_state",
      "id": "22222222-2222-4222-8222-222222222222",
      "data": {
        "state": "accepted",
        "acceptance_reference": "conversation:user-agreed-local-deployment"
      }
    },
    {
      "ref": "next-action",
      "type": "project.update",
      "data": { "next_step": "Review the system architecture" }
    }
  ]
}
```

All operations commit at revision 2, or all roll back. The response includes the project, checkpoint, and `results` mapping each `ref` to the generated/existing entity ID. A newly created document remains draft. New entities cannot be targeted by other operations in the same request; use their returned IDs in the next checkpoint.

## 4. Handle retries and competing edits

- Retry the exact save with the same key/body. A committed retry returns the original result and `Idempotency-Replayed: true`, even though its supplied revision is now older.
- Reusing the key for changed input returns 409 `idempotency_key_reused`.
- A different save using an old revision returns 409 `revision_conflict` with `expected_revision` and `current_revision` details.
- Reload current content, reconcile the changes, then submit the revised request with a new key. Do not simply replace the revision on stale content.

## 5. Inspect the history

```http
GET /api/v1/projects/<project_id>/history
Authorization: Bearer <agent-credential>
```

```http
GET /api/v1/projects/<project_id>/history/<checkpoint_id>
Authorization: Bearer <agent-credential>
```

History lists checkpoint summaries, revision, origin, and writer metadata. Detail includes ordered change items with before/after snapshots. A content update followed by explicit acceptance of that same document/decision produces one consolidated entity change; the acceptance reference is retained in metadata.

## 6. Archive, resume, and bind a workspace

Archive/unarchive are standalone checkpoints with one operation:

```json
{
  "expected_revision": 2,
  "summary": "Set this idea aside",
  "operations": [{ "ref": "archive", "type": "project.archive" }]
}
```

The new revision is 3. Archived projects remain readable; editing requires `project.unarchive` in a new standalone checkpoint using the current revision. List archived ideas using `archived=true` or include both with `archived=all`.

A workspace association uses `workspace.bind` with data such as `{ "workspace_key": "/Users/example/project", "repository_url": null }`. The directory path must be absolute and canonical. Resolve it using:

```http
GET /api/v1/workspace-bindings/resolve?workspace_key=%2FUsers%2Fexample%2Fproject
Authorization: Bearer <agent-credential>
```

A bound directory resolves to one project. Another binding to the same path returns 409; unbind explicitly before attaching it to another project.

## Browser callers

The same domain routes work with the dashboard session cookie. For writes, also provide `X-CSRF-Token` from `/api/v1/me`. Browser requests must respect the same-origin policy. The React interface and FastMCP wrapper have not been implemented yet.

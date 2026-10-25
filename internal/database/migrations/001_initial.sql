-- Application UUIDs are generated in Go. All deletion is restrictive unless explicit.
CREATE TABLE profiles (
    id uuid PRIMARY KEY,
    display_name text NOT NULL CHECK (length(btrim(display_name)) > 0),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE clients (
    id uuid PRIMARY KEY,
    profile_id uuid NOT NULL REFERENCES profiles(id),
    kind text NOT NULL CHECK (kind IN ('dashboard', 'agent', 'system')),
    name text NOT NULL CHECK (length(btrim(name)) > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz
);
CREATE INDEX clients_profile_idx ON clients(profile_id);
CREATE TABLE projects (
    id uuid PRIMARY KEY,
    profile_id uuid NOT NULL REFERENCES profiles(id),
    title text NOT NULL CHECK (length(btrim(title)) > 0),
    summary text NOT NULL DEFAULT '',
    next_step text NOT NULL DEFAULT '',
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    archived_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX projects_active_idx ON projects(profile_id, updated_at DESC, id DESC) WHERE archived_at IS NULL;
CREATE INDEX projects_archived_idx ON projects(profile_id, updated_at DESC, id DESC) WHERE archived_at IS NOT NULL;

CREATE TABLE documents (
    id uuid PRIMARY KEY,
    project_id uuid NOT NULL REFERENCES projects(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    last_changed_revision bigint NOT NULL CHECK (last_changed_revision > 0),
    kind text NOT NULL CHECK (kind IN ('overview', 'feature_plan', 'architecture', 'note')),
    title text NOT NULL CHECK (length(btrim(title)) > 0),
    content text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT 'draft' CHECK (state IN ('draft', 'accepted', 'superseded'))
);
CREATE UNIQUE INDEX documents_one_overview_idx ON documents(project_id) WHERE kind = 'overview';
CREATE INDEX documents_project_idx ON documents(project_id, kind, updated_at DESC, id DESC);

CREATE TABLE decisions (
    id uuid PRIMARY KEY,
    project_id uuid NOT NULL REFERENCES projects(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    last_changed_revision bigint NOT NULL CHECK (last_changed_revision > 0),
    title text NOT NULL CHECK (length(btrim(title)) > 0),
    statement text NOT NULL CHECK (length(btrim(statement)) > 0),
    rationale text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT 'proposed' CHECK (state IN ('proposed', 'accepted', 'rejected', 'superseded')),
    source_reference text,
    acceptance_reference text,
    CHECK (state <> 'accepted' OR (acceptance_reference IS NOT NULL AND length(btrim(acceptance_reference)) > 0))
);
CREATE INDEX decisions_context_idx ON decisions(project_id, state);

CREATE TABLE open_questions (
    id uuid PRIMARY KEY,
    project_id uuid NOT NULL REFERENCES projects(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    last_changed_revision bigint NOT NULL CHECK (last_changed_revision > 0),
    question text NOT NULL CHECK (length(btrim(question)) > 0),
    state text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'resolved', 'dismissed')),
    resolution text,
    CHECK ((state = 'open' AND resolution IS NULL) OR (state <> 'open' AND resolution IS NOT NULL AND length(btrim(resolution)) > 0))
);
CREATE INDEX questions_context_idx ON open_questions(project_id, state);

CREATE TABLE progress_entries (
    id uuid PRIMARY KEY,
    project_id uuid NOT NULL REFERENCES projects(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    last_changed_revision bigint NOT NULL CHECK (last_changed_revision > 0),
    summary text NOT NULL CHECK (length(btrim(summary)) > 0),
    verification text NOT NULL DEFAULT 'reported' CHECK (verification IN ('reported', 'verified')),
    evidence jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(evidence) = 'array'),
    CHECK (CASE WHEN verification = 'verified' THEN jsonb_array_length(evidence) > 0 ELSE true END)
);
CREATE INDEX progress_recent_idx ON progress_entries(project_id, created_at DESC, id DESC);

CREATE TABLE workspace_bindings (
    id uuid PRIMARY KEY,
    project_id uuid NOT NULL REFERENCES projects(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    last_changed_revision bigint NOT NULL CHECK (last_changed_revision > 0),
    workspace_key text NOT NULL UNIQUE CHECK (length(btrim(workspace_key)) > 0),
    repository_url text
);
CREATE INDEX bindings_project_idx ON workspace_bindings(project_id);
CREATE TABLE checkpoints (
    id uuid PRIMARY KEY,
    project_id uuid NOT NULL REFERENCES projects(id),
    revision bigint NOT NULL CHECK (revision > 0),
    client_id uuid NOT NULL REFERENCES clients(id),
    origin text NOT NULL CHECK (origin IN ('dashboard', 'mcp', 'system')),
    summary text NOT NULL CHECK (length(btrim(summary)) > 0),
    source_reference text,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(project_id, revision)
);
CREATE INDEX checkpoints_client_idx ON checkpoints(client_id);
CREATE TABLE change_items (
    id uuid PRIMARY KEY,
    checkpoint_id uuid NOT NULL REFERENCES checkpoints(id),
    ordinal integer NOT NULL CHECK (ordinal > 0),
    entity_type text NOT NULL CHECK (entity_type IN ('project', 'document', 'decision', 'question', 'progress', 'workspace_binding')),
    entity_id uuid NOT NULL,
    operation text NOT NULL CHECK (operation IN ('create', 'update', 'delete')),
    before_data jsonb CHECK (jsonb_typeof(before_data) = 'object'),
    after_data jsonb CHECK (jsonb_typeof(after_data) = 'object'),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    CHECK (
        (operation = 'create' AND before_data IS NULL AND after_data IS NOT NULL) OR
        (operation = 'update' AND before_data IS NOT NULL AND after_data IS NOT NULL) OR
        (operation = 'delete' AND before_data IS NOT NULL AND after_data IS NULL)
    ),
    UNIQUE(checkpoint_id, ordinal),
    UNIQUE(checkpoint_id, entity_type, entity_id)
);
CREATE TABLE write_requests (
    id uuid PRIMARY KEY,
    client_id uuid NOT NULL REFERENCES clients(id),
    idempotency_key text NOT NULL CHECK (octet_length(idempotency_key) BETWEEN 1 AND 128 AND idempotency_key ~ '^[!-~]+$'),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    response_status integer NOT NULL CHECK (response_status BETWEEN 200 AND 299),
    response_body jsonb NOT NULL CHECK (jsonb_typeof(response_body) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(client_id, idempotency_key)
);

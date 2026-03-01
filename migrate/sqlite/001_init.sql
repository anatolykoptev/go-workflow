CREATE TABLE IF NOT EXISTS workflows (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    owner           TEXT NOT NULL DEFAULT '',
    state           TEXT NOT NULL DEFAULT 'pending',
    idempotency_key TEXT,
    data            TEXT NOT NULL,
    created_at      INTEGER NOT NULL DEFAULT 0,
    updated_at      INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_workflows_state ON workflows(state);
CREATE INDEX IF NOT EXISTS idx_workflows_owner ON workflows(owner);

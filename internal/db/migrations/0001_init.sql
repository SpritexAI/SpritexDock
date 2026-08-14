CREATE TABLE users (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE applications (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    repository_url TEXT NOT NULL,
    branch TEXT NOT NULL,
    dockerfile_path TEXT NOT NULL,
    build_context TEXT NOT NULL,
    exposed_port INTEGER NOT NULL CHECK (exposed_port BETWEEN 1 AND 65535),
    generated_hostname TEXT NOT NULL UNIQUE,
    current_deployment_id TEXT,
    status TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE deployments (
    id TEXT PRIMARY KEY,
    application_id TEXT NOT NULL,
    revision_commit_sha TEXT NOT NULL,
    trigger_type TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('queued', 'building', 'deploying', 'running', 'failed', 'cancelled')),
    image_reference TEXT,
    started_at TEXT,
    finished_at TEXT,
    failure_reason TEXT,
    created_at TEXT NOT NULL,
    FOREIGN KEY (application_id) REFERENCES applications(id) ON DELETE CASCADE
);

CREATE TABLE domains (
    id TEXT PRIMARY KEY,
    application_id TEXT NOT NULL,
    hostname TEXT NOT NULL UNIQUE,
    type TEXT NOT NULL CHECK (type IN ('generated', 'custom')),
    verification_status TEXT NOT NULL,
    certificate_status_metadata TEXT,
    last_verification_error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (application_id) REFERENCES applications(id) ON DELETE CASCADE
);

CREATE TABLE environment_variables (
    id TEXT PRIMARY KEY,
    application_id TEXT NOT NULL,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    secret_flag INTEGER NOT NULL DEFAULT 0 CHECK (secret_flag IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (application_id) REFERENCES applications(id) ON DELETE CASCADE,
    UNIQUE (application_id, key)
);

CREATE INDEX idx_deployments_application_created
    ON deployments (application_id, created_at DESC);

CREATE INDEX idx_domains_application
    ON domains (application_id);

CREATE INDEX idx_environment_variables_application
    ON environment_variables (application_id);

-- Every read filters on expires_at, so expiry is enforced in queries rather than
-- by the database. The cleanup job only reclaims space.

create table if not exists content_items (
    id                       text        primary key,
    workspace_id             text        not null,
    type                     text        not null,
    status                   text        not null,
    working_title            text        not null,
    normalized_working_title text        not null,
    revision                 bigint      not null,
    created_at               timestamptz not null,
    updated_at               timestamptz not null,
    expires_at               timestamptz not null,
    scheduled_at             timestamptz,
    content                  jsonb       not null
);

create index if not exists content_items_workspace_expiry on content_items (workspace_id, expires_at);
create index if not exists content_items_workspace_type on content_items (workspace_id, type, expires_at);
create index if not exists content_items_workspace_status on content_items (workspace_id, status, expires_at);
create index if not exists content_items_workspace_scheduled on content_items (workspace_id, scheduled_at);
-- text_pattern_ops supports the prefix search the library uses.
create index if not exists content_items_workspace_title on content_items (workspace_id, normalized_working_title text_pattern_ops);

create table if not exists mutation_receipts (
    workspace_id text        not null,
    operation_id text        not null,
    request_hash text        not null,
    operation    text        not null,
    http_status  integer     not null,
    result       jsonb       not null,
    error_code   text        not null default '',
    expires_at   timestamptz not null,
    primary key (workspace_id, operation_id)
);

create index if not exists mutation_receipts_expiry on mutation_receipts (expires_at);

-- Session and login-attempt identifiers are stored as SHA-256 digests, never raw.
create table if not exists oauth_login_attempts (
    id            text        primary key,
    state         text        not null,
    code_verifier text        not null,
    expires_at    timestamptz not null
);

create index if not exists oauth_login_attempts_expiry on oauth_login_attempts (expires_at);

create table if not exists sessions (
    id           text        primary key,
    workspace_id text        not null,
    csrf_token   text        not null,
    expires_at   timestamptz not null
);

create index if not exists sessions_expiry on sessions (expires_at);

create table if not exists api_tokens (
    id           text        primary key,
    workspace_id text        not null,
    prefix       text        not null,
    hash         bytea       not null unique,
    scopes       text[]      not null,
    created_at   timestamptz not null
);

-- Distributed rate limiting. Rows are short lived and reclaimed by cleanup.
create table if not exists api_token_rate_limits (
    id                text        primary key,
    window_started_at timestamptz not null,
    count             integer     not null,
    expires_at        timestamptz not null
);

create index if not exists api_token_rate_limits_expiry on api_token_rate_limits (expires_at);

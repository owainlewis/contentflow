-- Password sign-in. Emails are stored lower-cased so uniqueness and lookup are
-- case-insensitive without depending on the citext extension.
create table if not exists users (
    id            text        primary key,
    workspace_id  text        not null,
    email         text        not null unique,
    password_hash text        not null,
    created_at    timestamptz not null
);

create index if not exists users_workspace on users (workspace_id);

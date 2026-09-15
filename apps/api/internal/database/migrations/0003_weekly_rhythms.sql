create table weekly_rhythms (
    workspace_id text primary key,
    revision bigint not null default 0,
    targets jsonb not null
);

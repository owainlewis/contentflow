-- Content is a permanent library. Retain the legacy wire timestamp for clients.
update content_items set expires_at = '9999-12-31T00:00:00Z';
alter table content_items add column topic_id text not null default '';
alter table content_items add column format text not null default '';
alter table content_items add column document_url text not null default '';
alter table content_items add column video_url text not null default '';
create index content_items_workspace_topic on content_items(workspace_id, topic_id);
-- Topic references are checked under serializable transactions so deletion and
-- attaching a piece cannot race into a dangling reference.
drop index content_items_workspace_expiry;
drop index content_items_workspace_type;
drop index content_items_workspace_status;
create index content_items_workspace_type on content_items(workspace_id, type);
create index content_items_workspace_status on content_items(workspace_id, status);

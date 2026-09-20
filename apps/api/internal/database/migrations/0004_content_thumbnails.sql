-- Composite foreign key keeps ownership explicit and removes images with content.
alter table content_items add constraint content_items_workspace_id_unique unique(workspace_id,id);
create table content_thumbnails (
 workspace_id text not null,
 content_id text not null,
 content_type text not null check (content_type in ('image/jpeg','image/png')),
 data bytea not null check (octet_length(data) between 1 and 5242880),
 primary key(workspace_id,content_id),
 foreign key(workspace_id,content_id) references content_items(workspace_id,id) on delete cascade
);

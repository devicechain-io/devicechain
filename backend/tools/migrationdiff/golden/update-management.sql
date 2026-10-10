ALTER SEQUENCE "update-management".audit_events_id_seq OWNED BY "update-management".audit_events.id;
ALTER TABLE ONLY "update-management".audit_events
 ADD CONSTRAINT audit_events_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "update-management".audit_events ALTER COLUMN id SET DEFAULT nextval('"update-management".audit_events_id_seq'::regclass);
ALTER TABLE ONLY "update-management".purged_tenants
 ADD CONSTRAINT purged_tenants_pkey PRIMARY KEY (token, epoch);
ALTER TABLE ONLY "update-management".update_management_migrations
 ADD CONSTRAINT update_management_migrations_pkey PRIMARY KEY (id);
CREATE INDEX idx_audit_tenant_time ON "update-management".audit_events USING btree (tenant_id, occurred_time DESC);
CREATE SCHEMA "update-management";
CREATE SEQUENCE "update-management".audit_events_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE TABLE "update-management".audit_events (
 id bigint NOT NULL,
 occurred_time timestamp with time zone NOT NULL,
 tenant_id text,
 category text NOT NULL,
 actor text,
 table_name text,
 operation text NOT NULL,
 entity_pk text,
 entity_label text,
 rows_affected bigint
);
CREATE TABLE "update-management".purged_tenants (
 token character varying(128) NOT NULL,
 epoch timestamp with time zone NOT NULL,
 planted_at timestamp with time zone NOT NULL,
 completed_at timestamp with time zone
);
CREATE TABLE "update-management".update_management_migrations (
 id character varying(255) NOT NULL
);

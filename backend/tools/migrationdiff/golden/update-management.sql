ALTER SEQUENCE "update-management".artifacts_id_seq OWNED BY "update-management".artifacts.id;
ALTER SEQUENCE "update-management".audit_events_id_seq OWNED BY "update-management".audit_events.id;
ALTER TABLE ONLY "update-management".artifacts
 ADD CONSTRAINT artifacts_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "update-management".artifacts ALTER COLUMN id SET DEFAULT nextval('"update-management".artifacts_id_seq'::regclass);
ALTER TABLE ONLY "update-management".audit_events
 ADD CONSTRAINT audit_events_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "update-management".audit_events ALTER COLUMN id SET DEFAULT nextval('"update-management".audit_events_id_seq'::regclass);
ALTER TABLE ONLY "update-management".purged_tenants
 ADD CONSTRAINT purged_tenants_pkey PRIMARY KEY (token, epoch);
ALTER TABLE ONLY "update-management".update_management_migrations
 ADD CONSTRAINT update_management_migrations_pkey PRIMARY KEY (id);
CREATE INDEX "idx_update-management_artifacts_deleted_at" ON "update-management".artifacts USING btree (deleted_at);
CREATE INDEX "idx_update-management_artifacts_tenant_id" ON "update-management".artifacts USING btree (tenant_id);
CREATE INDEX "idx_update-management_artifacts_token" ON "update-management".artifacts USING btree (token);
CREATE INDEX idx_audit_tenant_time ON "update-management".audit_events USING btree (tenant_id, occurred_time DESC);
CREATE SCHEMA "update-management";
CREATE SEQUENCE "update-management".artifacts_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "update-management".audit_events_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE TABLE "update-management".artifacts (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 kind character varying(16) NOT NULL,
 name character varying(128) NOT NULL,
 version character varying(64) NOT NULL,
 component character varying(64) NOT NULL,
 size_bytes bigint NOT NULL,
 digest character varying(71) NOT NULL,
 media_type character varying(127) NOT NULL,
 compatible_profiles jsonb NOT NULL,
 trust_status character varying(16) NOT NULL,
 provenance jsonb,
 blob_ref character varying(512),
 state character varying(16) NOT NULL,
 uploading_since timestamp with time zone
);
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
CREATE UNIQUE INDEX uix_artifacts_tenant_identity ON "update-management".artifacts USING btree (tenant_id, name, component, version) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_artifacts_tenant_token ON "update-management".artifacts USING btree (tenant_id, token) WHERE (deleted_at IS NULL);

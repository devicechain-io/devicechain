ALTER SEQUENCE "update-management".artifacts_id_seq OWNED BY "update-management".artifacts.id;
ALTER SEQUENCE "update-management".audit_events_id_seq OWNED BY "update-management".audit_events.id;
ALTER SEQUENCE "update-management".secrets_id_seq OWNED BY "update-management".secrets.id;
ALTER SEQUENCE "update-management".update_assignments_id_seq OWNED BY "update-management".update_assignments.id;
ALTER SEQUENCE "update-management".update_attempts_id_seq OWNED BY "update-management".update_attempts.id;
ALTER TABLE ONLY "update-management".artifacts
 ADD CONSTRAINT artifacts_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "update-management".artifacts ALTER COLUMN id SET DEFAULT nextval('"update-management".artifacts_id_seq'::regclass);
ALTER TABLE ONLY "update-management".audit_events
 ADD CONSTRAINT audit_events_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "update-management".audit_events ALTER COLUMN id SET DEFAULT nextval('"update-management".audit_events_id_seq'::regclass);
ALTER TABLE ONLY "update-management".purged_tenants
 ADD CONSTRAINT purged_tenants_pkey PRIMARY KEY (token, epoch);
ALTER TABLE ONLY "update-management".secrets
 ADD CONSTRAINT secrets_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "update-management".secrets ALTER COLUMN id SET DEFAULT nextval('"update-management".secrets_id_seq'::regclass);
ALTER TABLE ONLY "update-management".update_assignments
 ADD CONSTRAINT update_assignments_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "update-management".update_assignments ALTER COLUMN id SET DEFAULT nextval('"update-management".update_assignments_id_seq'::regclass);
ALTER TABLE ONLY "update-management".update_attempts
 ADD CONSTRAINT update_attempts_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "update-management".update_attempts ALTER COLUMN id SET DEFAULT nextval('"update-management".update_attempts_id_seq'::regclass);
ALTER TABLE ONLY "update-management".update_management_migrations
 ADD CONSTRAINT update_management_migrations_pkey PRIMARY KEY (id);
CREATE INDEX "idx_update-management_artifacts_deleted_at" ON "update-management".artifacts USING btree (deleted_at);
CREATE INDEX "idx_update-management_artifacts_tenant_id" ON "update-management".artifacts USING btree (tenant_id);
CREATE INDEX "idx_update-management_artifacts_token" ON "update-management".artifacts USING btree (token);
CREATE INDEX "idx_update-management_secrets_deleted_at" ON "update-management".secrets USING btree (deleted_at);
CREATE INDEX "idx_update-management_secrets_name" ON "update-management".secrets USING btree (name);
CREATE INDEX "idx_update-management_secrets_scope" ON "update-management".secrets USING btree (scope);
CREATE INDEX "idx_update-management_secrets_tenant_id" ON "update-management".secrets USING btree (tenant_id);
CREATE INDEX "idx_update-management_update_assignments_artifact_id" ON "update-management".update_assignments USING btree (artifact_id);
CREATE INDEX "idx_update-management_update_assignments_device_token" ON "update-management".update_assignments USING btree (device_token);
CREATE INDEX "idx_update-management_update_assignments_tenant_id" ON "update-management".update_assignments USING btree (tenant_id);
CREATE INDEX "idx_update-management_update_attempts_tenant_id" ON "update-management".update_attempts USING btree (tenant_id);
CREATE INDEX idx_audit_tenant_time ON "update-management".audit_events USING btree (tenant_id, occurred_time DESC);
CREATE INDEX ix_update_attempts_state_report ON "update-management".update_attempts USING btree (state, last_report_at);
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
CREATE SEQUENCE "update-management".secrets_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "update-management".update_assignments_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "update-management".update_attempts_id_seq
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
CREATE TABLE "update-management".secrets (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 scope character varying(16) NOT NULL,
 name character varying(256) NOT NULL,
 ciphertext bytea NOT NULL,
 nonce bytea NOT NULL,
 wrapped_dek bytea NOT NULL,
 kek_version bigint NOT NULL,
 alg character varying(32) NOT NULL
);
CREATE TABLE "update-management".update_assignments (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 device_token character varying(128) NOT NULL,
 component character varying(64) NOT NULL,
 artifact_id bigint NOT NULL,
 actor character varying(256),
 superseded_at timestamp with time zone
);
CREATE TABLE "update-management".update_attempts (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 assignment_id bigint NOT NULL,
 device_token character varying(128) NOT NULL,
 component character varying(64) NOT NULL,
 target_version character varying(64) NOT NULL,
 target_digest character varying(71) NOT NULL,
 requires_reboot boolean NOT NULL,
 state character varying(16) NOT NULL,
 version bigint NOT NULL,
 last_seq bigint NOT NULL,
 last_report_at timestamp with time zone NOT NULL,
 boot_id_at_start character varying(128) NOT NULL,
 install_boot_id character varying(128) NOT NULL,
 last_boot_id character varying(128) NOT NULL,
 last_stage character varying(16) NOT NULL,
 cancel_requested boolean NOT NULL,
 reconciled boolean NOT NULL,
 progress_bytes bigint,
 progress_total bigint,
 failure_stage character varying(16) NOT NULL,
 failure_code character varying(64) NOT NULL,
 failure_detail character varying(512) NOT NULL,
 failure_platform boolean NOT NULL
);
CREATE TABLE "update-management".update_management_migrations (
 id character varying(255) NOT NULL
);
CREATE UNIQUE INDEX uix_artifacts_tenant_identity ON "update-management".artifacts USING btree (tenant_id, name, component, version) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_artifacts_tenant_token ON "update-management".artifacts USING btree (tenant_id, token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_secrets_tenant_scope_name ON "update-management".secrets USING btree (tenant_id, scope, name) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_update_assignments_current ON "update-management".update_assignments USING btree (tenant_id, device_token, component) WHERE (superseded_at IS NULL);
CREATE UNIQUE INDEX uix_update_assignments_tenant_token ON "update-management".update_assignments USING btree (tenant_id, token);
CREATE UNIQUE INDEX uix_update_attempts_assignment ON "update-management".update_attempts USING btree (assignment_id);
CREATE UNIQUE INDEX uix_update_attempts_tenant_token ON "update-management".update_attempts USING btree (tenant_id, token);
ALTER SEQUENCE "outbound-connectors".audit_events_id_seq OWNED BY "outbound-connectors".audit_events.id;
ALTER SEQUENCE "outbound-connectors".connector_versions_id_seq OWNED BY "outbound-connectors".connector_versions.id;
ALTER SEQUENCE "outbound-connectors".connectors_id_seq OWNED BY "outbound-connectors".connectors.id;
ALTER SEQUENCE "outbound-connectors".secrets_id_seq OWNED BY "outbound-connectors".secrets.id;
ALTER TABLE ONLY "outbound-connectors".audit_events
 ADD CONSTRAINT audit_events_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "outbound-connectors".audit_events ALTER COLUMN id SET DEFAULT nextval('"outbound-connectors".audit_events_id_seq'::regclass);
ALTER TABLE ONLY "outbound-connectors".connector_versions
 ADD CONSTRAINT connector_versions_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "outbound-connectors".connector_versions ALTER COLUMN id SET DEFAULT nextval('"outbound-connectors".connector_versions_id_seq'::regclass);
ALTER TABLE ONLY "outbound-connectors".connectors
 ADD CONSTRAINT connectors_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "outbound-connectors".connectors ALTER COLUMN id SET DEFAULT nextval('"outbound-connectors".connectors_id_seq'::regclass);
ALTER TABLE ONLY "outbound-connectors".outbound_connectors_migrations
 ADD CONSTRAINT outbound_connectors_migrations_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "outbound-connectors".secrets
 ADD CONSTRAINT secrets_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "outbound-connectors".secrets ALTER COLUMN id SET DEFAULT nextval('"outbound-connectors".secrets_id_seq'::regclass);
CREATE INDEX "idx_outbound-connectors_connector_versions_deleted_at" ON "outbound-connectors".connector_versions USING btree (deleted_at);
CREATE INDEX "idx_outbound-connectors_connector_versions_tenant_id" ON "outbound-connectors".connector_versions USING btree (tenant_id);
CREATE INDEX "idx_outbound-connectors_connectors_deleted_at" ON "outbound-connectors".connectors USING btree (deleted_at);
CREATE INDEX "idx_outbound-connectors_connectors_tenant_id" ON "outbound-connectors".connectors USING btree (tenant_id);
CREATE INDEX "idx_outbound-connectors_connectors_token" ON "outbound-connectors".connectors USING btree (token);
CREATE INDEX "idx_outbound-connectors_secrets_deleted_at" ON "outbound-connectors".secrets USING btree (deleted_at);
CREATE INDEX "idx_outbound-connectors_secrets_name" ON "outbound-connectors".secrets USING btree (name);
CREATE INDEX "idx_outbound-connectors_secrets_scope" ON "outbound-connectors".secrets USING btree (scope);
CREATE INDEX "idx_outbound-connectors_secrets_tenant_id" ON "outbound-connectors".secrets USING btree (tenant_id);
CREATE INDEX idx_audit_tenant_time ON "outbound-connectors".audit_events USING btree (tenant_id, occurred_time DESC);
CREATE SCHEMA "outbound-connectors";
CREATE SEQUENCE "outbound-connectors".audit_events_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "outbound-connectors".connector_versions_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "outbound-connectors".connectors_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "outbound-connectors".secrets_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE TABLE "outbound-connectors".audit_events (
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
CREATE TABLE "outbound-connectors".connector_versions (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 connector_id bigint NOT NULL,
 version integer NOT NULL,
 type character varying(64) NOT NULL,
 config jsonb NOT NULL,
 label character varying(128),
 description character varying(1024),
 published_by character varying(256)
);
CREATE TABLE "outbound-connectors".connectors (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 name character varying(128),
 description character varying(1024),
 type character varying(64) NOT NULL,
 config jsonb NOT NULL
);
CREATE TABLE "outbound-connectors".outbound_connectors_migrations (
 id character varying(255) NOT NULL
);
CREATE TABLE "outbound-connectors".secrets (
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
CREATE UNIQUE INDEX uix_connector_versions_connector_version ON "outbound-connectors".connector_versions USING btree (connector_id, version);
CREATE UNIQUE INDEX uix_connectors_tenant_token ON "outbound-connectors".connectors USING btree (tenant_id, token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_secrets_tenant_scope_name ON "outbound-connectors".secrets USING btree (tenant_id, scope, name) WHERE (deleted_at IS NULL);

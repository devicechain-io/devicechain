ALTER SEQUENCE "ai-inference".ai_provider_tenant_grants_id_seq OWNED BY "ai-inference".ai_provider_tenant_grants.id;
ALTER SEQUENCE "ai-inference".ai_provider_tier_grants_id_seq OWNED BY "ai-inference".ai_provider_tier_grants.id;
ALTER SEQUENCE "ai-inference".ai_providers_id_seq OWNED BY "ai-inference".ai_providers.id;
ALTER SEQUENCE "ai-inference".audit_events_id_seq OWNED BY "ai-inference".audit_events.id;
ALTER SEQUENCE "ai-inference".secrets_id_seq OWNED BY "ai-inference".secrets.id;
ALTER TABLE ONLY "ai-inference".ai_inference_migrations
 ADD CONSTRAINT ai_inference_migrations_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "ai-inference".ai_provider_tenant_grants
 ADD CONSTRAINT ai_provider_tenant_grants_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "ai-inference".ai_provider_tenant_grants
 ADD CONSTRAINT fk_ai_provider_tenant_grants_provider FOREIGN KEY (provider_id) REFERENCES "ai-inference".ai_providers(id) ON DELETE RESTRICT;
ALTER TABLE ONLY "ai-inference".ai_provider_tenant_grants ALTER COLUMN id SET DEFAULT nextval('"ai-inference".ai_provider_tenant_grants_id_seq'::regclass);
ALTER TABLE ONLY "ai-inference".ai_provider_tier_grants
 ADD CONSTRAINT ai_provider_tier_grants_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "ai-inference".ai_provider_tier_grants
 ADD CONSTRAINT fk_ai_provider_tier_grants_provider FOREIGN KEY (provider_id) REFERENCES "ai-inference".ai_providers(id) ON DELETE RESTRICT;
ALTER TABLE ONLY "ai-inference".ai_provider_tier_grants ALTER COLUMN id SET DEFAULT nextval('"ai-inference".ai_provider_tier_grants_id_seq'::regclass);
ALTER TABLE ONLY "ai-inference".ai_providers
 ADD CONSTRAINT ai_providers_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "ai-inference".ai_providers ALTER COLUMN id SET DEFAULT nextval('"ai-inference".ai_providers_id_seq'::regclass);
ALTER TABLE ONLY "ai-inference".audit_events
 ADD CONSTRAINT audit_events_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "ai-inference".audit_events ALTER COLUMN id SET DEFAULT nextval('"ai-inference".audit_events_id_seq'::regclass);
ALTER TABLE ONLY "ai-inference".secrets
 ADD CONSTRAINT secrets_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "ai-inference".secrets ALTER COLUMN id SET DEFAULT nextval('"ai-inference".secrets_id_seq'::regclass);
CREATE INDEX "idx_ai-inference_secrets_deleted_at" ON "ai-inference".secrets USING btree (deleted_at);
CREATE INDEX "idx_ai-inference_secrets_name" ON "ai-inference".secrets USING btree (name);
CREATE INDEX "idx_ai-inference_secrets_scope" ON "ai-inference".secrets USING btree (scope);
CREATE INDEX "idx_ai-inference_secrets_tenant_id" ON "ai-inference".secrets USING btree (tenant_id);
CREATE INDEX idx_ai_provider_tenant_grants_provider_id ON "ai-inference".ai_provider_tenant_grants USING btree (provider_id);
CREATE INDEX idx_ai_provider_tenant_grants_tenant_id ON "ai-inference".ai_provider_tenant_grants USING btree (tenant_id);
CREATE INDEX idx_ai_provider_tier_grants_provider_id ON "ai-inference".ai_provider_tier_grants USING btree (provider_id);
CREATE INDEX idx_ai_providers_deleted_at ON "ai-inference".ai_providers USING btree (deleted_at);
CREATE INDEX idx_ai_providers_token ON "ai-inference".ai_providers USING btree (token);
CREATE INDEX idx_audit_tenant_time ON "ai-inference".audit_events USING btree (tenant_id, occurred_time DESC);
CREATE SCHEMA "ai-inference";
CREATE SEQUENCE "ai-inference".ai_provider_tenant_grants_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "ai-inference".ai_provider_tier_grants_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "ai-inference".ai_providers_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "ai-inference".audit_events_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "ai-inference".secrets_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE TABLE "ai-inference".ai_inference_migrations (
 id character varying(255) NOT NULL
);
CREATE TABLE "ai-inference".ai_provider_tenant_grants (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 provider_id bigint NOT NULL
);
CREATE TABLE "ai-inference".ai_provider_tier_grants (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 tier_token character varying(128) NOT NULL,
 provider_id bigint NOT NULL,
 is_default boolean NOT NULL
);
CREATE TABLE "ai-inference".ai_providers (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 token character varying(128) NOT NULL,
 name character varying(128),
 description character varying(1024),
 kind character varying(64) NOT NULL,
 endpoint character varying(512),
 model character varying(128) NOT NULL,
 params jsonb,
 enabled boolean NOT NULL
);
CREATE TABLE "ai-inference".audit_events (
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
CREATE TABLE "ai-inference".secrets (
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
CREATE UNIQUE INDEX uix_ai_providers_token ON "ai-inference".ai_providers USING btree (token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_ai_tenant_grant_pair ON "ai-inference".ai_provider_tenant_grants USING btree (tenant_id, provider_id);
CREATE UNIQUE INDEX uix_ai_tier_grant_default ON "ai-inference".ai_provider_tier_grants USING btree (tier_token) WHERE is_default;
CREATE UNIQUE INDEX uix_ai_tier_grant_pair ON "ai-inference".ai_provider_tier_grants USING btree (tier_token, provider_id);
CREATE UNIQUE INDEX uix_secrets_tenant_scope_name ON "ai-inference".secrets USING btree (tenant_id, scope, name) WHERE (deleted_at IS NULL);

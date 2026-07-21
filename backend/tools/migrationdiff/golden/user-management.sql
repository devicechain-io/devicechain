ALTER SEQUENCE "user-management".audit_events_id_seq OWNED BY "user-management".audit_events.id;
ALTER SEQUENCE "user-management".iam_identities_id_seq OWNED BY "user-management".iam_identities.id;
ALTER SEQUENCE "user-management".iam_memberships_id_seq OWNED BY "user-management".iam_memberships.id;
ALTER SEQUENCE "user-management".iam_oauth_clients_id_seq OWNED BY "user-management".iam_oauth_clients.id;
ALTER SEQUENCE "user-management".iam_roles_id_seq OWNED BY "user-management".iam_roles.id;
ALTER SEQUENCE "user-management".iam_tenant_tiers_id_seq OWNED BY "user-management".iam_tenant_tiers.id;
ALTER SEQUENCE "user-management".iam_tenants_id_seq OWNED BY "user-management".iam_tenants.id;
ALTER SEQUENCE "user-management".signing_keys_id_seq OWNED BY "user-management".signing_keys.id;
ALTER TABLE ONLY "user-management".audit_events
 ADD CONSTRAINT audit_events_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".audit_events ALTER COLUMN id SET DEFAULT nextval('"user-management".audit_events_id_seq'::regclass);
ALTER TABLE ONLY "user-management".iam_identities
 ADD CONSTRAINT iam_identities_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".iam_identities ALTER COLUMN id SET DEFAULT nextval('"user-management".iam_identities_id_seq'::regclass);
ALTER TABLE ONLY "user-management".iam_identity_system_roles
 ADD CONSTRAINT "fk_user-management_iam_identity_system_roles_identity" FOREIGN KEY (identity_id) REFERENCES "user-management".iam_identities(id);
ALTER TABLE ONLY "user-management".iam_identity_system_roles
 ADD CONSTRAINT "fk_user-management_iam_identity_system_roles_role" FOREIGN KEY (role_id) REFERENCES "user-management".iam_roles(id);
ALTER TABLE ONLY "user-management".iam_identity_system_roles
 ADD CONSTRAINT iam_identity_system_roles_pkey PRIMARY KEY (identity_id, role_id);
ALTER TABLE ONLY "user-management".iam_membership_tenant_roles
 ADD CONSTRAINT "fk_user-management_iam_membership_tenant_roles_membership" FOREIGN KEY (membership_id) REFERENCES "user-management".iam_memberships(id);
ALTER TABLE ONLY "user-management".iam_membership_tenant_roles
 ADD CONSTRAINT "fk_user-management_iam_membership_tenant_roles_role" FOREIGN KEY (role_id) REFERENCES "user-management".iam_roles(id);
ALTER TABLE ONLY "user-management".iam_membership_tenant_roles
 ADD CONSTRAINT iam_membership_tenant_roles_pkey PRIMARY KEY (membership_id, role_id);
ALTER TABLE ONLY "user-management".iam_memberships
 ADD CONSTRAINT fk_iam_identities_memberships FOREIGN KEY (identity_id) REFERENCES "user-management".iam_identities(id);
ALTER TABLE ONLY "user-management".iam_memberships
 ADD CONSTRAINT iam_memberships_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".iam_memberships ALTER COLUMN id SET DEFAULT nextval('"user-management".iam_memberships_id_seq'::regclass);
ALTER TABLE ONLY "user-management".iam_oauth_clients
 ADD CONSTRAINT iam_oauth_clients_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".iam_oauth_clients ALTER COLUMN id SET DEFAULT nextval('"user-management".iam_oauth_clients_id_seq'::regclass);
ALTER TABLE ONLY "user-management".iam_roles
 ADD CONSTRAINT iam_roles_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".iam_roles ALTER COLUMN id SET DEFAULT nextval('"user-management".iam_roles_id_seq'::regclass);
ALTER TABLE ONLY "user-management".iam_tenant_tiers
 ADD CONSTRAINT iam_tenant_tiers_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".iam_tenant_tiers ALTER COLUMN id SET DEFAULT nextval('"user-management".iam_tenant_tiers_id_seq'::regclass);
ALTER TABLE ONLY "user-management".iam_tenants
 ADD CONSTRAINT fk_iam_tenants_tier FOREIGN KEY (tier_id) REFERENCES "user-management".iam_tenant_tiers(id) ON DELETE RESTRICT;
ALTER TABLE ONLY "user-management".iam_tenants
 ADD CONSTRAINT iam_tenants_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".iam_tenants ALTER COLUMN id SET DEFAULT nextval('"user-management".iam_tenants_id_seq'::regclass);
ALTER TABLE ONLY "user-management".signing_keys
 ADD CONSTRAINT signing_keys_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".signing_keys ALTER COLUMN id SET DEFAULT nextval('"user-management".signing_keys_id_seq'::regclass);
ALTER TABLE ONLY "user-management".system_settings
 ADD CONSTRAINT system_settings_pkey PRIMARY KEY (key);
ALTER TABLE ONLY "user-management".user_management_migrations
 ADD CONSTRAINT user_management_migrations_pkey PRIMARY KEY (id);
CREATE INDEX "idx_user-management_signing_keys_active" ON "user-management".signing_keys USING btree (active);
CREATE INDEX "idx_user-management_signing_keys_deleted_at" ON "user-management".signing_keys USING btree (deleted_at);
CREATE INDEX "idx_user-management_signing_keys_retired_at" ON "user-management".signing_keys USING btree (retired_at);
CREATE INDEX idx_audit_tenant_time ON "user-management".audit_events USING btree (tenant_id, occurred_time DESC);
CREATE INDEX idx_iam_identities_deleted_at ON "user-management".iam_identities USING btree (deleted_at);
CREATE INDEX idx_iam_memberships_deleted_at ON "user-management".iam_memberships USING btree (deleted_at);
CREATE INDEX idx_iam_oauth_clients_deleted_at ON "user-management".iam_oauth_clients USING btree (deleted_at);
CREATE INDEX idx_iam_roles_deleted_at ON "user-management".iam_roles USING btree (deleted_at);
CREATE INDEX idx_iam_tenant_tiers_deleted_at ON "user-management".iam_tenant_tiers USING btree (deleted_at);
CREATE INDEX idx_iam_tenants_deleted_at ON "user-management".iam_tenants USING btree (deleted_at);
CREATE INDEX idx_iam_tenants_tier_id ON "user-management".iam_tenants USING btree (tier_id);
CREATE SCHEMA "user-management";
CREATE SEQUENCE "user-management".audit_events_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "user-management".iam_identities_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "user-management".iam_memberships_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "user-management".iam_oauth_clients_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "user-management".iam_roles_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "user-management".iam_tenant_tiers_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "user-management".iam_tenants_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "user-management".signing_keys_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE TABLE "user-management".audit_events (
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
CREATE TABLE "user-management".iam_identities (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 email character varying(256) NOT NULL,
 first_name character varying(128),
 last_name character varying(128),
 enabled boolean DEFAULT true NOT NULL,
 password_hash character varying(256) NOT NULL
);
CREATE TABLE "user-management".iam_identity_system_roles (
 identity_id bigint NOT NULL,
 role_id bigint NOT NULL
);
CREATE TABLE "user-management".iam_membership_tenant_roles (
 membership_id bigint NOT NULL,
 role_id bigint NOT NULL
);
CREATE TABLE "user-management".iam_memberships (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 identity_id bigint NOT NULL,
 tenant_id character varying(128) NOT NULL,
 enabled boolean DEFAULT true NOT NULL
);
CREATE TABLE "user-management".iam_oauth_clients (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 name character varying(128),
 description character varying(1024),
 client_id character varying(128) NOT NULL,
 redirect_uris text,
 scopes text,
 enabled boolean DEFAULT true NOT NULL,
 secret_hash character varying(100)
);
CREATE TABLE "user-management".iam_roles (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 name character varying(128),
 description character varying(1024),
 scope character varying(16) NOT NULL,
 token character varying(128) NOT NULL,
 authorities text
);
CREATE TABLE "user-management".iam_tenant_tiers (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 name character varying(128),
 description character varying(1024),
 token character varying(128) NOT NULL,
 config text,
 display_order bigint DEFAULT 0 NOT NULL,
 color character varying(32) DEFAULT ''::character varying NOT NULL
);
CREATE TABLE "user-management".iam_tenants (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 name character varying(128),
 description character varying(1024),
 token character varying(128) NOT NULL,
 enabled boolean DEFAULT true NOT NULL,
 config text,
 tier_id bigint NOT NULL,
 ingest_messages_per_second numeric,
 ingest_burst bigint,
 outbound_messages_per_second numeric,
 outbound_burst bigint,
 branding_title text,
 branding_logo text,
 branding_logo_max_height bigint,
 branding_primary text,
 branding_background text,
 branding_foreground text,
 branding_accent text,
 ai_external_enabled boolean,
 ai_inference_requests_per_minute numeric,
 ai_inference_burst bigint,
 shed_priority bigint
);
CREATE TABLE "user-management".signing_keys (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 active boolean DEFAULT true NOT NULL,
 private_key_pem text NOT NULL,
 public_key_pem text NOT NULL,
 retired_at timestamp with time zone
);
CREATE TABLE "user-management".system_settings (
 key character varying(190) NOT NULL,
 value jsonb NOT NULL,
 updated_at timestamp with time zone,
 updated_by character varying(190)
);
CREATE TABLE "user-management".user_management_migrations (
 id character varying(255) NOT NULL
);
CREATE UNIQUE INDEX idx_iam_identities_email ON "user-management".iam_identities USING btree (email);
CREATE UNIQUE INDEX idx_iam_membership_identity_tenant ON "user-management".iam_memberships USING btree (identity_id, tenant_id);
CREATE UNIQUE INDEX idx_iam_oauth_clients_client_id ON "user-management".iam_oauth_clients USING btree (client_id);
CREATE UNIQUE INDEX idx_iam_role_scope_token ON "user-management".iam_roles USING btree (scope, token);
CREATE UNIQUE INDEX idx_iam_tenant_tiers_token ON "user-management".iam_tenant_tiers USING btree (token);
CREATE UNIQUE INDEX idx_iam_tenants_token ON "user-management".iam_tenants USING btree (token);

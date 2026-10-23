ALTER SEQUENCE "user-management".audit_events_id_seq OWNED BY "user-management".audit_events.id;
ALTER SEQUENCE "user-management".dead_letters_id_seq OWNED BY "user-management".dead_letters.id;
ALTER SEQUENCE "user-management".iam_identities_id_seq OWNED BY "user-management".iam_identities.id;
ALTER SEQUENCE "user-management".iam_identity_links_id_seq OWNED BY "user-management".iam_identity_links.id;
ALTER SEQUENCE "user-management".iam_idp_connection_domains_id_seq OWNED BY "user-management".iam_idp_connection_domains.id;
ALTER SEQUENCE "user-management".iam_idp_connections_id_seq OWNED BY "user-management".iam_idp_connections.id;
ALTER SEQUENCE "user-management".iam_idp_role_mappings_id_seq OWNED BY "user-management".iam_idp_role_mappings.id;
ALTER SEQUENCE "user-management".iam_invitations_id_seq OWNED BY "user-management".iam_invitations.id;
ALTER SEQUENCE "user-management".iam_memberships_id_seq OWNED BY "user-management".iam_memberships.id;
ALTER SEQUENCE "user-management".iam_oauth_clients_id_seq OWNED BY "user-management".iam_oauth_clients.id;
ALTER SEQUENCE "user-management".iam_roles_id_seq OWNED BY "user-management".iam_roles.id;
ALTER SEQUENCE "user-management".iam_service_account_secrets_id_seq OWNED BY "user-management".iam_service_account_secrets.id;
ALTER SEQUENCE "user-management".iam_service_accounts_id_seq OWNED BY "user-management".iam_service_accounts.id;
ALTER SEQUENCE "user-management".iam_sso_link_requests_id_seq OWNED BY "user-management".iam_sso_link_requests.id;
ALTER SEQUENCE "user-management".iam_tenant_purge_stores_id_seq OWNED BY "user-management".iam_tenant_purge_stores.id;
ALTER SEQUENCE "user-management".iam_tenant_purges_id_seq OWNED BY "user-management".iam_tenant_purges.id;
ALTER SEQUENCE "user-management".iam_tenant_tiers_id_seq OWNED BY "user-management".iam_tenant_tiers.id;
ALTER SEQUENCE "user-management".iam_tenants_id_seq OWNED BY "user-management".iam_tenants.id;
ALTER SEQUENCE "user-management".secrets_id_seq OWNED BY "user-management".secrets.id;
ALTER SEQUENCE "user-management".signing_keys_id_seq OWNED BY "user-management".signing_keys.id;
ALTER TABLE ONLY "user-management".audit_events
 ADD CONSTRAINT audit_events_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".audit_events ALTER COLUMN id SET DEFAULT nextval('"user-management".audit_events_id_seq'::regclass);
ALTER TABLE ONLY "user-management".dead_letters
 ADD CONSTRAINT dead_letters_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".dead_letters ALTER COLUMN id SET DEFAULT nextval('"user-management".dead_letters_id_seq'::regclass);
ALTER TABLE ONLY "user-management".iam_identities
 ADD CONSTRAINT iam_identities_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".iam_identities ALTER COLUMN id SET DEFAULT nextval('"user-management".iam_identities_id_seq'::regclass);
ALTER TABLE ONLY "user-management".iam_identity_links
 ADD CONSTRAINT fk_iam_identity_links_connection FOREIGN KEY (connection_id) REFERENCES "user-management".iam_idp_connections(id) ON DELETE CASCADE;
ALTER TABLE ONLY "user-management".iam_identity_links
 ADD CONSTRAINT fk_iam_identity_links_identity FOREIGN KEY (identity_id) REFERENCES "user-management".iam_identities(id) ON DELETE CASCADE;
ALTER TABLE ONLY "user-management".iam_identity_links
 ADD CONSTRAINT iam_identity_links_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".iam_identity_links ALTER COLUMN id SET DEFAULT nextval('"user-management".iam_identity_links_id_seq'::regclass);
ALTER TABLE ONLY "user-management".iam_identity_system_roles
 ADD CONSTRAINT "fk_user-management_iam_identity_system_roles_identity" FOREIGN KEY (identity_id) REFERENCES "user-management".iam_identities(id);
ALTER TABLE ONLY "user-management".iam_identity_system_roles
 ADD CONSTRAINT "fk_user-management_iam_identity_system_roles_role" FOREIGN KEY (role_id) REFERENCES "user-management".iam_roles(id);
ALTER TABLE ONLY "user-management".iam_identity_system_roles
 ADD CONSTRAINT iam_identity_system_roles_pkey PRIMARY KEY (identity_id, role_id);
ALTER TABLE ONLY "user-management".iam_idp_connection_domains
 ADD CONSTRAINT fk_iam_idp_connection_domains_connection FOREIGN KEY (connection_id) REFERENCES "user-management".iam_idp_connections(id) ON DELETE CASCADE;
ALTER TABLE ONLY "user-management".iam_idp_connection_domains
 ADD CONSTRAINT iam_idp_connection_domains_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".iam_idp_connection_domains ALTER COLUMN id SET DEFAULT nextval('"user-management".iam_idp_connection_domains_id_seq'::regclass);
ALTER TABLE ONLY "user-management".iam_idp_connection_tenants
 ADD CONSTRAINT fk_iam_idp_connection_tenants_connection FOREIGN KEY (connection_id) REFERENCES "user-management".iam_idp_connections(id) ON DELETE CASCADE;
ALTER TABLE ONLY "user-management".iam_idp_connection_tenants
 ADD CONSTRAINT iam_idp_connection_tenants_pkey PRIMARY KEY (connection_id, tenant_id);
ALTER TABLE ONLY "user-management".iam_idp_connections
 ADD CONSTRAINT iam_idp_connections_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".iam_idp_connections ALTER COLUMN id SET DEFAULT nextval('"user-management".iam_idp_connections_id_seq'::regclass);
ALTER TABLE ONLY "user-management".iam_idp_role_mappings
 ADD CONSTRAINT fk_iam_idp_connection_tenants_mappings FOREIGN KEY (connection_id, tenant_id) REFERENCES "user-management".iam_idp_connection_tenants(connection_id, tenant_id) ON DELETE CASCADE;
ALTER TABLE ONLY "user-management".iam_idp_role_mappings
 ADD CONSTRAINT fk_iam_idp_role_mappings_role FOREIGN KEY (role_id) REFERENCES "user-management".iam_roles(id) ON DELETE RESTRICT;
ALTER TABLE ONLY "user-management".iam_idp_role_mappings
 ADD CONSTRAINT iam_idp_role_mappings_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".iam_idp_role_mappings ALTER COLUMN id SET DEFAULT nextval('"user-management".iam_idp_role_mappings_id_seq'::regclass);
ALTER TABLE ONLY "user-management".iam_invitations
 ADD CONSTRAINT fk_iam_invitations_inviter FOREIGN KEY (invited_by) REFERENCES "user-management".iam_identities(id) ON DELETE SET NULL;
ALTER TABLE ONLY "user-management".iam_invitations
 ADD CONSTRAINT fk_iam_invitations_membership FOREIGN KEY (membership_id) REFERENCES "user-management".iam_memberships(id) ON DELETE CASCADE;
ALTER TABLE ONLY "user-management".iam_invitations
 ADD CONSTRAINT iam_invitations_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".iam_invitations ALTER COLUMN id SET DEFAULT nextval('"user-management".iam_invitations_id_seq'::regclass);
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
ALTER TABLE ONLY "user-management".iam_service_account_roles
 ADD CONSTRAINT fk_iam_service_account_roles_role FOREIGN KEY (role_id) REFERENCES "user-management".iam_roles(id) ON DELETE CASCADE;
ALTER TABLE ONLY "user-management".iam_service_account_roles
 ADD CONSTRAINT fk_iam_service_account_roles_service_account FOREIGN KEY (service_account_id) REFERENCES "user-management".iam_service_accounts(id) ON DELETE CASCADE;
ALTER TABLE ONLY "user-management".iam_service_account_roles
 ADD CONSTRAINT iam_service_account_roles_pkey PRIMARY KEY (service_account_id, role_id);
ALTER TABLE ONLY "user-management".iam_service_account_secrets
 ADD CONSTRAINT fk_iam_service_account_secrets_service_account FOREIGN KEY (service_account_id) REFERENCES "user-management".iam_service_accounts(id) ON DELETE CASCADE;
ALTER TABLE ONLY "user-management".iam_service_account_secrets
 ADD CONSTRAINT iam_service_account_secrets_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".iam_service_account_secrets ALTER COLUMN id SET DEFAULT nextval('"user-management".iam_service_account_secrets_id_seq'::regclass);
ALTER TABLE ONLY "user-management".iam_service_accounts
 ADD CONSTRAINT iam_service_accounts_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".iam_service_accounts ALTER COLUMN id SET DEFAULT nextval('"user-management".iam_service_accounts_id_seq'::regclass);
ALTER TABLE ONLY "user-management".iam_sso_link_requests
 ADD CONSTRAINT fk_iam_sso_link_requests_connection FOREIGN KEY (connection_id) REFERENCES "user-management".iam_idp_connections(id) ON DELETE CASCADE;
ALTER TABLE ONLY "user-management".iam_sso_link_requests
 ADD CONSTRAINT iam_sso_link_requests_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".iam_sso_link_requests ALTER COLUMN id SET DEFAULT nextval('"user-management".iam_sso_link_requests_id_seq'::regclass);
ALTER TABLE ONLY "user-management".iam_tenant_purge_stores
 ADD CONSTRAINT fk_iam_tenant_purge_stores_tenant_purge FOREIGN KEY (tenant_purge_id) REFERENCES "user-management".iam_tenant_purges(id) ON DELETE CASCADE;
ALTER TABLE ONLY "user-management".iam_tenant_purge_stores
 ADD CONSTRAINT iam_tenant_purge_stores_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".iam_tenant_purge_stores ALTER COLUMN id SET DEFAULT nextval('"user-management".iam_tenant_purge_stores_id_seq'::regclass);
ALTER TABLE ONLY "user-management".iam_tenant_purges
 ADD CONSTRAINT iam_tenant_purges_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".iam_tenant_purges ALTER COLUMN id SET DEFAULT nextval('"user-management".iam_tenant_purges_id_seq'::regclass);
ALTER TABLE ONLY "user-management".iam_tenant_tiers
 ADD CONSTRAINT iam_tenant_tiers_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".iam_tenant_tiers ALTER COLUMN id SET DEFAULT nextval('"user-management".iam_tenant_tiers_id_seq'::regclass);
ALTER TABLE ONLY "user-management".iam_tenants
 ADD CONSTRAINT fk_iam_tenants_tier FOREIGN KEY (tier_id) REFERENCES "user-management".iam_tenant_tiers(id) ON DELETE RESTRICT;
ALTER TABLE ONLY "user-management".iam_tenants
 ADD CONSTRAINT iam_tenants_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".iam_tenants ALTER COLUMN id SET DEFAULT nextval('"user-management".iam_tenants_id_seq'::regclass);
ALTER TABLE ONLY "user-management".purged_tenants
 ADD CONSTRAINT purged_tenants_pkey PRIMARY KEY (token, epoch);
ALTER TABLE ONLY "user-management".secrets
 ADD CONSTRAINT secrets_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".secrets ALTER COLUMN id SET DEFAULT nextval('"user-management".secrets_id_seq'::regclass);
ALTER TABLE ONLY "user-management".signing_keys
 ADD CONSTRAINT signing_keys_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "user-management".signing_keys ALTER COLUMN id SET DEFAULT nextval('"user-management".signing_keys_id_seq'::regclass);
ALTER TABLE ONLY "user-management".system_settings
 ADD CONSTRAINT system_settings_pkey PRIMARY KEY (key);
ALTER TABLE ONLY "user-management".user_management_migrations
 ADD CONSTRAINT user_management_migrations_pkey PRIMARY KEY (id);
CREATE INDEX "idx_user-management_secrets_deleted_at" ON "user-management".secrets USING btree (deleted_at);
CREATE INDEX "idx_user-management_secrets_name" ON "user-management".secrets USING btree (name);
CREATE INDEX "idx_user-management_secrets_scope" ON "user-management".secrets USING btree (scope);
CREATE INDEX "idx_user-management_secrets_tenant_id" ON "user-management".secrets USING btree (tenant_id);
CREATE INDEX "idx_user-management_signing_keys_active" ON "user-management".signing_keys USING btree (active);
CREATE INDEX "idx_user-management_signing_keys_deleted_at" ON "user-management".signing_keys USING btree (deleted_at);
CREATE INDEX "idx_user-management_signing_keys_retired_at" ON "user-management".signing_keys USING btree (retired_at);
CREATE INDEX idx_audit_tenant_time ON "user-management".audit_events USING btree (tenant_id, occurred_time DESC);
CREATE INDEX idx_dead_letters_kind_time ON "user-management".dead_letters USING btree (kind, occurred_time DESC);
CREATE INDEX idx_dead_letters_tenant_time ON "user-management".dead_letters USING btree (tenant_id, occurred_time DESC);
CREATE INDEX idx_dead_letters_time ON "user-management".dead_letters USING btree (occurred_time DESC);
CREATE INDEX idx_iam_identities_deleted_at ON "user-management".iam_identities USING btree (deleted_at);
CREATE INDEX idx_iam_idp_connection_domains_connection ON "user-management".iam_idp_connection_domains USING btree (connection_id);
CREATE INDEX idx_iam_invitations_invited_by ON "user-management".iam_invitations USING btree (invited_by);
CREATE INDEX idx_iam_invitations_membership ON "user-management".iam_invitations USING btree (membership_id);
CREATE INDEX idx_iam_invitations_tenant ON "user-management".iam_invitations USING btree (tenant_id);
CREATE INDEX idx_iam_memberships_deleted_at ON "user-management".iam_memberships USING btree (deleted_at);
CREATE INDEX idx_iam_memberships_managed_by ON "user-management".iam_memberships USING btree (managed_by);
CREATE INDEX idx_iam_oauth_clients_deleted_at ON "user-management".iam_oauth_clients USING btree (deleted_at);
CREATE INDEX idx_iam_roles_deleted_at ON "user-management".iam_roles USING btree (deleted_at);
CREATE INDEX idx_iam_service_account_secrets_account ON "user-management".iam_service_account_secrets USING btree (service_account_id);
CREATE INDEX idx_iam_tenant_purges_completed_at ON "user-management".iam_tenant_purges USING btree (completed_at);
CREATE INDEX idx_iam_tenant_tiers_deleted_at ON "user-management".iam_tenant_tiers USING btree (deleted_at);
CREATE INDEX idx_iam_tenants_deleted_at ON "user-management".iam_tenants USING btree (deleted_at);
CREATE INDEX idx_iam_tenants_purge_state ON "user-management".iam_tenants USING btree (purge_state);
CREATE INDEX idx_iam_tenants_tier_id ON "user-management".iam_tenants USING btree (tier_id);
CREATE SCHEMA "user-management";
CREATE SEQUENCE "user-management".audit_events_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "user-management".dead_letters_id_seq
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
CREATE SEQUENCE "user-management".iam_identity_links_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "user-management".iam_idp_connection_domains_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "user-management".iam_idp_connections_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "user-management".iam_idp_role_mappings_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "user-management".iam_invitations_id_seq
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
CREATE SEQUENCE "user-management".iam_service_account_secrets_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "user-management".iam_service_accounts_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "user-management".iam_sso_link_requests_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "user-management".iam_tenant_purge_stores_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "user-management".iam_tenant_purges_id_seq
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
CREATE SEQUENCE "user-management".secrets_id_seq
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
CREATE TABLE "user-management".dead_letters (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 occurred_time timestamp with time zone NOT NULL,
 kind character varying(64) NOT NULL,
 reason character varying(32) NOT NULL,
 source character varying(64) NOT NULL,
 summary text NOT NULL,
 detail text,
 attempts bigint,
 subject character varying(512),
 sequence bigint,
 correlation character varying(128),
 reference character varying(256),
 stream_seq bigint NOT NULL,
 append_time timestamp with time zone NOT NULL,
 payload bytea
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
 password_hash character varying(256) NOT NULL,
 session_epoch character varying(64) DEFAULT ''::character varying NOT NULL
);
CREATE TABLE "user-management".iam_identity_links (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 identity_id bigint NOT NULL,
 connection_id bigint NOT NULL,
 subject character varying(512) NOT NULL,
 created_by character varying(256) DEFAULT ''::character varying NOT NULL
);
CREATE TABLE "user-management".iam_identity_system_roles (
 identity_id bigint NOT NULL,
 role_id bigint NOT NULL
);
CREATE TABLE "user-management".iam_idp_connection_domains (
 id bigint NOT NULL,
 domain character varying(253) NOT NULL,
 connection_id bigint NOT NULL,
 verified_at timestamp with time zone NOT NULL,
 verified_by character varying(256) NOT NULL
);
CREATE TABLE "user-management".iam_idp_connection_tenants (
 connection_id bigint NOT NULL,
 tenant_id character varying(128) NOT NULL
);
CREATE TABLE "user-management".iam_idp_connections (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 token character varying(128) NOT NULL,
 name character varying(256) NOT NULL,
 kind character varying(16) NOT NULL,
 enabled boolean DEFAULT false NOT NULL,
 issuer character varying(512) NOT NULL,
 client_id character varying(256) NOT NULL,
 client_secret_ref character varying(256) DEFAULT ''::character varying NOT NULL,
 scopes text,
 email_claim character varying(64) DEFAULT 'email'::character varying NOT NULL,
 groups_claim character varying(64) DEFAULT ''::character varying NOT NULL,
 required_claims text,
 required_amr text,
 jit_enabled boolean DEFAULT false NOT NULL,
 default_tenant_roles text,
 session_max_age_seconds bigint DEFAULT 43200 NOT NULL,
 discovery_snapshot text,
 discovery_approved_at timestamp with time zone,
 discovery_approved_by character varying(256) DEFAULT ''::character varying NOT NULL,
 provider_sign_out boolean DEFAULT false NOT NULL,
 ca_bundle text DEFAULT ''::text NOT NULL
);
CREATE TABLE "user-management".iam_idp_role_mappings (
 id bigint NOT NULL,
 connection_id bigint NOT NULL,
 tenant_id character varying(128) NOT NULL,
 group_value character varying(512) NOT NULL,
 role_id bigint NOT NULL
);
CREATE TABLE "user-management".iam_invitations (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 membership_id bigint NOT NULL,
 token_digest bytea NOT NULL,
 invited_by bigint,
 expires_at timestamp with time zone NOT NULL,
 accepted_at timestamp with time zone,
 revoked_at timestamp with time zone
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
 enabled boolean DEFAULT true NOT NULL,
 managed_by bigint,
 scope jsonb
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
 authorities text,
 tenant_id character varying(128)
);
CREATE TABLE "user-management".iam_service_account_roles (
 service_account_id bigint NOT NULL,
 role_id bigint NOT NULL
);
CREATE TABLE "user-management".iam_service_account_secrets (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 service_account_id bigint NOT NULL,
 digest character varying(128) NOT NULL,
 hint character varying(8) NOT NULL,
 expires_at timestamp with time zone NOT NULL,
 last_used_at timestamp with time zone
);
CREATE TABLE "user-management".iam_service_accounts (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 name character varying(128),
 description character varying(1024),
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 client_id character varying(64) NOT NULL,
 enabled boolean NOT NULL,
 scope jsonb,
 created_by character varying(256)
);
CREATE TABLE "user-management".iam_sso_link_requests (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 connection_id bigint NOT NULL,
 subject character varying(512) NOT NULL,
 email character varying(256) NOT NULL,
 claims_digest character varying(128) DEFAULT ''::character varying NOT NULL
);
CREATE TABLE "user-management".iam_tenant_purge_stores (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 tenant_purge_id bigint NOT NULL,
 store character varying(32) NOT NULL,
 complete boolean DEFAULT false NOT NULL,
 rows bigint DEFAULT 0 NOT NULL,
 deferred text,
 failure text,
 attempted_at timestamp with time zone,
 clean_since timestamp with time zone,
 note text
);
CREATE TABLE "user-management".iam_tenant_purges (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 token character varying(128) NOT NULL,
 epoch timestamp with time zone NOT NULL,
 completed_at timestamp with time zone,
 rows bigint DEFAULT 0 NOT NULL
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
 color character varying(32)
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
 ingest_readings_per_second numeric,
 ingest_burst bigint,
 outbound_calls_per_second numeric,
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
 shed_priority bigint,
 purge_state character varying(16) DEFAULT 'active'::character varying NOT NULL,
 purge_epoch timestamp with time zone,
 basemap_tile_url text,
 basemap_attribution text,
 basemap_center_lat numeric,
 basemap_center_lon numeric,
 basemap_zoom numeric,
 held_command_ceiling bigint,
 geo_fence_position_ceiling bigint,
 geo_fence_ceiling bigint,
 geo_fence_position_budget bigint,
 locale text,
 sso_enforced boolean DEFAULT false NOT NULL
);
CREATE TABLE "user-management".purged_tenants (
 token character varying(128) NOT NULL,
 epoch timestamp with time zone NOT NULL,
 planted_at timestamp with time zone NOT NULL,
 completed_at timestamp with time zone
);
CREATE TABLE "user-management".secrets (
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
CREATE TABLE "user-management".signing_keys (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 active boolean DEFAULT true NOT NULL,
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
CREATE UNIQUE INDEX idx_dead_letters_stream_seq ON "user-management".dead_letters USING btree (stream_seq, append_time);
CREATE UNIQUE INDEX idx_iam_identities_email ON "user-management".iam_identities USING btree (email);
CREATE UNIQUE INDEX idx_iam_identity_links_connection_identity ON "user-management".iam_identity_links USING btree (connection_id, identity_id);
CREATE UNIQUE INDEX idx_iam_identity_links_connection_subject ON "user-management".iam_identity_links USING btree (connection_id, subject);
CREATE UNIQUE INDEX idx_iam_idp_connection_domains_domain ON "user-management".iam_idp_connection_domains USING btree (domain);
CREATE UNIQUE INDEX idx_iam_idp_connections_token ON "user-management".iam_idp_connections USING btree (token);
CREATE UNIQUE INDEX idx_iam_idp_role_mappings_unique ON "user-management".iam_idp_role_mappings USING btree (connection_id, tenant_id, group_value, role_id);
CREATE UNIQUE INDEX idx_iam_invitations_digest ON "user-management".iam_invitations USING btree (token_digest);
CREATE UNIQUE INDEX idx_iam_membership_identity_tenant ON "user-management".iam_memberships USING btree (identity_id, tenant_id);
CREATE UNIQUE INDEX idx_iam_oauth_clients_client_id ON "user-management".iam_oauth_clients USING btree (client_id);
CREATE UNIQUE INDEX idx_iam_roles_catalogue_token ON "user-management".iam_roles USING btree (scope, token) WHERE (tenant_id IS NULL);
CREATE UNIQUE INDEX idx_iam_roles_owned_token ON "user-management".iam_roles USING btree (scope, tenant_id, token) WHERE (tenant_id IS NOT NULL);
CREATE UNIQUE INDEX idx_iam_service_accounts_client_id ON "user-management".iam_service_accounts USING btree (client_id);
CREATE UNIQUE INDEX idx_iam_service_accounts_tenant_token ON "user-management".iam_service_accounts USING btree (tenant_id, token);
CREATE UNIQUE INDEX idx_iam_sso_link_requests_connection_subject ON "user-management".iam_sso_link_requests USING btree (connection_id, subject);
CREATE UNIQUE INDEX idx_iam_tenant_tiers_token ON "user-management".iam_tenant_tiers USING btree (token);
CREATE UNIQUE INDEX idx_iam_tenants_token ON "user-management".iam_tenants USING btree (token);
CREATE UNIQUE INDEX idx_tenant_purge_stores_purge_store ON "user-management".iam_tenant_purge_stores USING btree (tenant_purge_id, store);
CREATE UNIQUE INDEX idx_tenant_purges_token_epoch ON "user-management".iam_tenant_purges USING btree (token, epoch);
CREATE UNIQUE INDEX uix_secrets_tenant_scope_name ON "user-management".secrets USING btree (tenant_id, scope, name) WHERE (deleted_at IS NULL);

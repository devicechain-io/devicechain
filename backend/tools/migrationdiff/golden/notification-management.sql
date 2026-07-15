ALTER SEQUENCE "notification-management".audit_events_id_seq OWNED BY "notification-management".audit_events.id;
ALTER SEQUENCE "notification-management".notification_channels_id_seq OWNED BY "notification-management".notification_channels.id;
ALTER SEQUENCE "notification-management".notification_policies_id_seq OWNED BY "notification-management".notification_policies.id;
ALTER SEQUENCE "notification-management".notification_rules_id_seq OWNED BY "notification-management".notification_rules.id;
ALTER SEQUENCE "notification-management".notification_states_id_seq OWNED BY "notification-management".notification_states.id;
ALTER SEQUENCE "notification-management".secrets_id_seq OWNED BY "notification-management".secrets.id;
ALTER TABLE ONLY "notification-management".audit_events
 ADD CONSTRAINT audit_events_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "notification-management".audit_events ALTER COLUMN id SET DEFAULT nextval('"notification-management".audit_events_id_seq'::regclass);
ALTER TABLE ONLY "notification-management".notification_channels
 ADD CONSTRAINT notification_channels_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "notification-management".notification_channels ALTER COLUMN id SET DEFAULT nextval('"notification-management".notification_channels_id_seq'::regclass);
ALTER TABLE ONLY "notification-management".notification_management_migrations
 ADD CONSTRAINT notification_management_migrations_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "notification-management".notification_policies
 ADD CONSTRAINT notification_policies_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "notification-management".notification_policies ALTER COLUMN id SET DEFAULT nextval('"notification-management".notification_policies_id_seq'::regclass);
ALTER TABLE ONLY "notification-management".notification_rules
 ADD CONSTRAINT notification_rules_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "notification-management".notification_rules ALTER COLUMN id SET DEFAULT nextval('"notification-management".notification_rules_id_seq'::regclass);
ALTER TABLE ONLY "notification-management".notification_states
 ADD CONSTRAINT notification_states_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "notification-management".notification_states ALTER COLUMN id SET DEFAULT nextval('"notification-management".notification_states_id_seq'::regclass);
ALTER TABLE ONLY "notification-management".secrets
 ADD CONSTRAINT secrets_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "notification-management".secrets ALTER COLUMN id SET DEFAULT nextval('"notification-management".secrets_id_seq'::regclass);
CREATE INDEX "idx_notification-management_notification_channels_channel_type" ON "notification-management".notification_channels USING btree (channel_type);
CREATE INDEX "idx_notification-management_notification_channels_deleted_at" ON "notification-management".notification_channels USING btree (deleted_at);
CREATE INDEX "idx_notification-management_notification_channels_tenant_id" ON "notification-management".notification_channels USING btree (tenant_id);
CREATE INDEX "idx_notification-management_notification_channels_token" ON "notification-management".notification_channels USING btree (token);
CREATE INDEX "idx_notification-management_notification_policies_deleted_at" ON "notification-management".notification_policies USING btree (deleted_at);
CREATE INDEX "idx_notification-management_notification_policies_devic2f007773" ON "notification-management".notification_policies USING btree (device_type_token);
CREATE INDEX "idx_notification-management_notification_policies_tenant_id" ON "notification-management".notification_policies USING btree (tenant_id);
CREATE INDEX "idx_notification-management_notification_policies_token" ON "notification-management".notification_policies USING btree (token);
CREATE INDEX "idx_notification-management_notification_rules_channel_id" ON "notification-management".notification_rules USING btree (channel_id);
CREATE INDEX "idx_notification-management_notification_rules_deleted_at" ON "notification-management".notification_rules USING btree (deleted_at);
CREATE INDEX "idx_notification-management_notification_rules_policy_id" ON "notification-management".notification_rules USING btree (policy_id);
CREATE INDEX "idx_notification-management_notification_rules_tenant_id" ON "notification-management".notification_rules USING btree (tenant_id);
CREATE INDEX "idx_notification-management_notification_states_alarm_key" ON "notification-management".notification_states USING btree (alarm_key);
CREATE INDEX "idx_notification-management_notification_states_deleted_at" ON "notification-management".notification_states USING btree (deleted_at);
CREATE INDEX "idx_notification-management_notification_states_tenant_id" ON "notification-management".notification_states USING btree (tenant_id);
CREATE INDEX "idx_notification-management_secrets_deleted_at" ON "notification-management".secrets USING btree (deleted_at);
CREATE INDEX "idx_notification-management_secrets_name" ON "notification-management".secrets USING btree (name);
CREATE INDEX "idx_notification-management_secrets_scope" ON "notification-management".secrets USING btree (scope);
CREATE INDEX "idx_notification-management_secrets_tenant_id" ON "notification-management".secrets USING btree (tenant_id);
CREATE INDEX idx_audit_tenant_time ON "notification-management".audit_events USING btree (tenant_id, occurred_time DESC);
CREATE SCHEMA "notification-management";
CREATE SEQUENCE "notification-management".audit_events_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "notification-management".notification_channels_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "notification-management".notification_policies_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "notification-management".notification_rules_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "notification-management".notification_states_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "notification-management".secrets_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE TABLE "notification-management".audit_events (
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
CREATE TABLE "notification-management".notification_channels (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 name character varying(128),
 description character varying(1024),
 metadata jsonb,
 channel_type character varying(32) NOT NULL,
 config jsonb,
 enabled boolean DEFAULT true NOT NULL
);
CREATE TABLE "notification-management".notification_management_migrations (
 id character varying(255) NOT NULL
);
CREATE TABLE "notification-management".notification_policies (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 name character varying(128),
 description character varying(1024),
 metadata jsonb,
 device_type_token character varying(128),
 throttle_seconds bigint,
 enabled boolean DEFAULT true NOT NULL,
 escalate_after_seconds bigint,
 max_escalations bigint
);
CREATE TABLE "notification-management".notification_rules (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 policy_id bigint NOT NULL,
 severity character varying(16) NOT NULL,
 channel_id bigint NOT NULL,
 recipients jsonb
);
CREATE TABLE "notification-management".notification_states (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 alarm_token character varying(128) NOT NULL,
 alarm_key character varying(256) NOT NULL,
 severity character varying(16),
 first_notified_at timestamp with time zone,
 last_notified_at timestamp with time zone,
 notify_count bigint DEFAULT 0 NOT NULL,
 acknowledged_at timestamp with time zone,
 cleared_at timestamp with time zone,
 escalation_level bigint DEFAULT 0 NOT NULL,
 last_escalated_at timestamp with time zone
);
CREATE TABLE "notification-management".secrets (
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
CREATE UNIQUE INDEX uix_notification_channels_tenant_token ON "notification-management".notification_channels USING btree (tenant_id, token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_notification_policies_tenant_token ON "notification-management".notification_policies USING btree (tenant_id, token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_notification_states_tenant_alarm_token ON "notification-management".notification_states USING btree (tenant_id, alarm_token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_secrets_tenant_scope_name ON "notification-management".secrets USING btree (tenant_id, scope, name) WHERE (deleted_at IS NULL);

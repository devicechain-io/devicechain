ALTER SEQUENCE "command-delivery".audit_events_id_seq OWNED BY "command-delivery".audit_events.id;
ALTER SEQUENCE "command-delivery".command_batches_id_seq OWNED BY "command-delivery".command_batches.id;
ALTER SEQUENCE "command-delivery".commands_id_seq OWNED BY "command-delivery".commands.id;
ALTER SEQUENCE "command-delivery".device_gate_syncs_id_seq OWNED BY "command-delivery".device_gate_syncs.id;
ALTER SEQUENCE "command-delivery".device_gates_id_seq OWNED BY "command-delivery".device_gates.id;
ALTER TABLE ONLY "command-delivery".audit_events
 ADD CONSTRAINT audit_events_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "command-delivery".audit_events ALTER COLUMN id SET DEFAULT nextval('"command-delivery".audit_events_id_seq'::regclass);
ALTER TABLE ONLY "command-delivery".command_batches
 ADD CONSTRAINT command_batches_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "command-delivery".command_batches ALTER COLUMN id SET DEFAULT nextval('"command-delivery".command_batches_id_seq'::regclass);
ALTER TABLE ONLY "command-delivery".command_delivery_migrations
 ADD CONSTRAINT command_delivery_migrations_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "command-delivery".commands
 ADD CONSTRAINT commands_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "command-delivery".commands ALTER COLUMN id SET DEFAULT nextval('"command-delivery".commands_id_seq'::regclass);
ALTER TABLE ONLY "command-delivery".device_gate_syncs
 ADD CONSTRAINT device_gate_syncs_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "command-delivery".device_gate_syncs ALTER COLUMN id SET DEFAULT nextval('"command-delivery".device_gate_syncs_id_seq'::regclass);
ALTER TABLE ONLY "command-delivery".device_gates
 ADD CONSTRAINT device_gates_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "command-delivery".device_gates ALTER COLUMN id SET DEFAULT nextval('"command-delivery".device_gates_id_seq'::regclass);
ALTER TABLE ONLY "command-delivery".purged_tenants
 ADD CONSTRAINT purged_tenants_pkey PRIMARY KEY (token, epoch);
CREATE INDEX "idx_command-delivery_command_batches_deleted_at" ON "command-delivery".command_batches USING btree (deleted_at);
CREATE INDEX "idx_command-delivery_command_batches_tenant_id" ON "command-delivery".command_batches USING btree (tenant_id);
CREATE INDEX "idx_command-delivery_command_batches_token" ON "command-delivery".command_batches USING btree (token);
CREATE INDEX "idx_command-delivery_commands_deleted_at" ON "command-delivery".commands USING btree (deleted_at);
CREATE INDEX "idx_command-delivery_commands_device_token" ON "command-delivery".commands USING btree (device_token);
CREATE INDEX "idx_command-delivery_commands_status" ON "command-delivery".commands USING btree (status);
CREATE INDEX "idx_command-delivery_commands_token" ON "command-delivery".commands USING btree (token);
CREATE INDEX idx_audit_tenant_time ON "command-delivery".audit_events USING btree (tenant_id, occurred_time DESC);
CREATE INDEX idx_command_batches_retry_of ON "command-delivery".command_batches USING btree (tenant_id, retry_of) WHERE (retry_of IS NOT NULL);
CREATE INDEX idx_commands_batch_token ON "command-delivery".commands USING btree (tenant_id, batch_token) WHERE ((batch_token IS NOT NULL) AND (deleted_at IS NULL));
CREATE INDEX idx_commands_device_time ON "command-delivery".commands USING btree (tenant_id, device_token, COALESCE(sent_time, queued_time)) WHERE (deleted_at IS NULL);
CREATE INDEX idx_commands_dispatchable_status ON "command-delivery".commands USING btree (status, id) WHERE ((deleted_at IS NULL) AND ((status)::text = ANY ((ARRAY['QUEUED'::character varying, 'HELD'::character varying])::text[])));
CREATE INDEX idx_commands_drainable ON "command-delivery".commands USING btree (tenant_id, device_token, id) WHERE ((deleted_at IS NULL) AND ((status)::text = ANY ((ARRAY['HELD'::character varying, 'PARKED'::character varying])::text[])));
CREATE INDEX idx_commands_queued_device ON "command-delivery".commands USING btree (tenant_id, device_token, id) WHERE ((deleted_at IS NULL) AND ((status)::text = 'QUEUED'::text));
CREATE INDEX idx_commands_scope_anchors ON "command-delivery".commands USING gin (scope_anchors);
CREATE INDEX idx_commands_stranded_sent ON "command-delivery".commands USING btree (sent_time, id) WHERE ((deleted_at IS NULL) AND ((status)::text = 'SENT'::text));
CREATE INDEX idx_commands_tenant_status ON "command-delivery".commands USING btree (tenant_id, status) WHERE (deleted_at IS NULL);
CREATE INDEX idx_commands_window_waiting ON "command-delivery".commands USING btree (tenant_id, window_token) WHERE ((deleted_at IS NULL) AND (window_token IS NOT NULL) AND ((status)::text = ANY ((ARRAY['QUEUED'::character varying, 'HELD'::character varying, 'PARKED'::character varying])::text[])));
CREATE INDEX idx_device_gates_disabled ON "command-delivery".device_gates USING btree (tenant_id, device_token) WHERE disabled;
CREATE SCHEMA "command-delivery";
CREATE SEQUENCE "command-delivery".audit_events_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "command-delivery".command_batches_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "command-delivery".commands_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "command-delivery".device_gate_syncs_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "command-delivery".device_gates_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE TABLE "command-delivery".audit_events (
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
CREATE TABLE "command-delivery".command_batches (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 metadata jsonb,
 name character varying(128) NOT NULL,
 payload jsonb,
 target_kind character varying(16) NOT NULL,
 group_token character varying(128),
 group_version integer,
 allow_partial boolean NOT NULL,
 resolved bigint NOT NULL,
 accepted bigint NOT NULL,
 refusals jsonb,
 refusal_counts jsonb,
 cancelled_at timestamp with time zone,
 cancelled_count integer,
 retry_of character varying(128),
 waiting integer
);
CREATE TABLE "command-delivery".command_delivery_migrations (
 id character varying(255) NOT NULL
);
CREATE TABLE "command-delivery".commands (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 metadata jsonb,
 device_token character varying(128) NOT NULL,
 name character varying(128) NOT NULL,
 payload jsonb,
 status character varying(32) NOT NULL,
 queued_time timestamp with time zone,
 sent_time timestamp with time zone,
 responded_time timestamp with time zone,
 expires_at timestamp with time zone,
 response_payload jsonb,
 error text,
 batch_id bigint,
 batch_token character varying(128),
 dispatch_nonce character varying(64),
 dispatch_failures integer DEFAULT 0 NOT NULL,
 outcome character varying(16),
 dispatch_after timestamp with time zone,
 dispatch_until timestamp with time zone,
 window_token character varying(128),
 scope_anchors text[]
);
CREATE TABLE "command-delivery".device_gate_syncs (
 id bigint NOT NULL,
 tenant_id character varying(128) NOT NULL,
 synced_at timestamp with time zone
);
CREATE TABLE "command-delivery".device_gates (
 id bigint NOT NULL,
 tenant_id character varying(128) NOT NULL,
 device_token character varying(128) NOT NULL,
 disabled boolean NOT NULL,
 updated_at timestamp with time zone
);
CREATE TABLE "command-delivery".purged_tenants (
 token character varying(128) NOT NULL,
 epoch timestamp with time zone NOT NULL,
 planted_at timestamp with time zone NOT NULL,
 completed_at timestamp with time zone
);
CREATE UNIQUE INDEX uix_command_batches_tenant_token ON "command-delivery".command_batches USING btree (tenant_id, token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_commands_batch_device ON "command-delivery".commands USING btree (tenant_id, batch_id, device_token) WHERE ((batch_id IS NOT NULL) AND (deleted_at IS NULL));
CREATE UNIQUE INDEX uix_commands_tenant_token ON "command-delivery".commands USING btree (tenant_id, token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_device_gate_syncs_tenant ON "command-delivery".device_gate_syncs USING btree (tenant_id);
CREATE UNIQUE INDEX uix_device_gates_tenant_device ON "command-delivery".device_gates USING btree (tenant_id, device_token);

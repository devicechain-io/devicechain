ALTER SEQUENCE "command-delivery".audit_events_id_seq OWNED BY "command-delivery".audit_events.id;
ALTER SEQUENCE "command-delivery".commands_id_seq OWNED BY "command-delivery".commands.id;
ALTER TABLE ONLY "command-delivery".audit_events
 ADD CONSTRAINT audit_events_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "command-delivery".audit_events ALTER COLUMN id SET DEFAULT nextval('"command-delivery".audit_events_id_seq'::regclass);
ALTER TABLE ONLY "command-delivery".command_delivery_migrations
 ADD CONSTRAINT command_delivery_migrations_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "command-delivery".commands
 ADD CONSTRAINT commands_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "command-delivery".commands ALTER COLUMN id SET DEFAULT nextval('"command-delivery".commands_id_seq'::regclass);
CREATE INDEX "idx_command-delivery_commands_deleted_at" ON "command-delivery".commands USING btree (deleted_at);
CREATE INDEX "idx_command-delivery_commands_device_token" ON "command-delivery".commands USING btree (device_token);
CREATE INDEX "idx_command-delivery_commands_status" ON "command-delivery".commands USING btree (status);
CREATE INDEX "idx_command-delivery_commands_tenant_id" ON "command-delivery".commands USING btree (tenant_id);
CREATE INDEX "idx_command-delivery_commands_token" ON "command-delivery".commands USING btree (token);
CREATE INDEX idx_audit_tenant_time ON "command-delivery".audit_events USING btree (tenant_id, occurred_time DESC);
CREATE SCHEMA "command-delivery";
CREATE SEQUENCE "command-delivery".audit_events_id_seq
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
 error text
);
CREATE UNIQUE INDEX uix_commands_tenant_token ON "command-delivery".commands USING btree (tenant_id, token) WHERE (deleted_at IS NULL);

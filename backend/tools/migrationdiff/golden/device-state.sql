ALTER SEQUENCE "device-state".audit_events_id_seq OWNED BY "device-state".audit_events.id;
ALTER SEQUENCE "device-state".device_states_id_seq OWNED BY "device-state".device_states.id;
ALTER SEQUENCE "device-state".latest_measurements_id_seq OWNED BY "device-state".latest_measurements.id;
ALTER TABLE ONLY "device-state".audit_events
 ADD CONSTRAINT audit_events_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-state".audit_events ALTER COLUMN id SET DEFAULT nextval('"device-state".audit_events_id_seq'::regclass);
ALTER TABLE ONLY "device-state".device_state_migrations
 ADD CONSTRAINT device_state_migrations_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-state".device_states
 ADD CONSTRAINT device_states_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-state".device_states ALTER COLUMN id SET DEFAULT nextval('"device-state".device_states_id_seq'::regclass);
ALTER TABLE ONLY "device-state".latest_measurements
 ADD CONSTRAINT latest_measurements_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-state".latest_measurements ALTER COLUMN id SET DEFAULT nextval('"device-state".latest_measurements_id_seq'::regclass);
CREATE INDEX "idx_device-state_device_states_deleted_at" ON "device-state".device_states USING btree (deleted_at);
CREATE INDEX "idx_device-state_latest_measurements_deleted_at" ON "device-state".latest_measurements USING btree (deleted_at);
CREATE INDEX "idx_device-state_latest_measurements_tenant_id" ON "device-state".latest_measurements USING btree (tenant_id);
CREATE INDEX idx_audit_tenant_time ON "device-state".audit_events USING btree (tenant_id, occurred_time DESC);
CREATE SCHEMA "device-state";
CREATE SEQUENCE "device-state".audit_events_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-state".device_states_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-state".latest_measurements_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE TABLE "device-state".audit_events (
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
CREATE TABLE "device-state".device_state_migrations (
 id character varying(255) NOT NULL
);
CREATE TABLE "device-state".device_states (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 device_token character varying(128) NOT NULL,
 active boolean DEFAULT false NOT NULL,
 last_connect_time timestamp with time zone,
 last_disconnect_time timestamp with time zone,
 last_activity_time timestamp with time zone,
 inactivity_alarm_time timestamp with time zone,
 inactivity_timeout bigint DEFAULT 600 NOT NULL,
 presence_source character varying(16) DEFAULT 'INFERRED'::character varying NOT NULL,
 session_id bigint DEFAULT 0 NOT NULL,
 presence_time timestamp with time zone,
 external_id text
);
CREATE TABLE "device-state".latest_measurements (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 device_token character varying(128) NOT NULL,
 name character varying(128) NOT NULL,
 value numeric(20,8),
 classifier bigint,
 occurred_time timestamp with time zone NOT NULL,
 unit text,
 data_type character varying(32)
);
CREATE UNIQUE INDEX idx_device_state_tenant_token ON "device-state".device_states USING btree (tenant_id, device_token);
CREATE UNIQUE INDEX idx_latest_measurement_tenant_device_name ON "device-state".latest_measurements USING btree (tenant_id, device_token, name);

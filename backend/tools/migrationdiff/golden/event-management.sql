ALTER SEQUENCE "event-management".audit_events_id_seq OWNED BY "event-management".audit_events.id;
ALTER TABLE ONLY "event-management".audit_events
 ADD CONSTRAINT audit_events_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "event-management".audit_events ALTER COLUMN id SET DEFAULT nextval('"event-management".audit_events_id_seq'::regclass);
ALTER TABLE ONLY "event-management".event_management_migrations
 ADD CONSTRAINT event_management_migrations_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "event-management".events
 ADD CONSTRAINT events_pkey PRIMARY KEY (tenant_id, device_token, event_type, occurred_time);
CREATE INDEX "idx_event-management_alert_events_tenant_id" ON "event-management".alert_events USING btree (tenant_id);
CREATE INDEX "idx_event-management_event_anchors_tenant_id" ON "event-management".event_anchors USING btree (tenant_id);
CREATE INDEX "idx_event-management_location_events_tenant_id" ON "event-management".location_events USING btree (tenant_id);
CREATE INDEX "idx_event-management_measurement_events_tenant_id" ON "event-management".measurement_events USING btree (tenant_id);
CREATE INDEX "idx_event-management_state_change_events_tenant_id" ON "event-management".state_change_events USING btree (tenant_id);
CREATE INDEX alert_events_occurred_time_idx ON "event-management".alert_events USING btree (occurred_time DESC);
CREATE INDEX alert_events_tenant_id_occurred_time_idx ON "event-management".alert_events USING btree (tenant_id, occurred_time DESC);
CREATE INDEX event_anchors_occurred_time_idx ON "event-management".event_anchors USING btree (occurred_time DESC);
CREATE INDEX events_device_token_occurred_time_idx ON "event-management".events USING btree (device_token, occurred_time DESC);
CREATE INDEX events_occurred_time_idx ON "event-management".events USING btree (occurred_time DESC);
CREATE INDEX events_tenant_id_occurred_time_idx ON "event-management".events USING btree (tenant_id, occurred_time DESC);
CREATE INDEX idx_audit_tenant_time ON "event-management".audit_events USING btree (tenant_id, occurred_time DESC);
CREATE INDEX idx_event_anchors_lookup ON "event-management".event_anchors USING btree (tenant_id, anchor_type, anchor_token, occurred_time DESC);
CREATE INDEX idx_measurement_tenant_device_name_time ON "event-management".measurement_events USING btree (tenant_id, device_token, name, occurred_time DESC);
CREATE INDEX idx_state_change_events_lookup ON "event-management".state_change_events USING btree (tenant_id, device_token, occurred_time DESC);
CREATE INDEX location_events_occurred_time_idx ON "event-management".location_events USING btree (occurred_time DESC);
CREATE INDEX location_events_tenant_id_occurred_time_idx ON "event-management".location_events USING btree (tenant_id, occurred_time DESC);
CREATE INDEX measurement_events_occurred_time_idx ON "event-management".measurement_events USING btree (occurred_time DESC);
CREATE INDEX measurement_events_tenant_id_occurred_time_idx ON "event-management".measurement_events USING btree (tenant_id, occurred_time DESC);
CREATE INDEX state_change_events_occurred_time_idx ON "event-management".state_change_events USING btree (occurred_time DESC);
CREATE SCHEMA "event-management";
CREATE SEQUENCE "event-management".audit_events_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE TABLE "event-management".alert_events (
 tenant_id character varying(128) NOT NULL,
 device_token character varying(128) NOT NULL COLLATE pg_catalog."C",
 event_type bigint NOT NULL,
 occurred_time timestamp with time zone NOT NULL,
 type text NOT NULL,
 level bigint NOT NULL,
 message text,
 source text
);
CREATE TABLE "event-management".audit_events (
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
CREATE TABLE "event-management".event_anchors (
 tenant_id character varying(128) NOT NULL,
 device_token character varying(128) NOT NULL COLLATE pg_catalog."C",
 event_type bigint NOT NULL,
 occurred_time timestamp with time zone NOT NULL,
 anchor_type text NOT NULL,
 anchor_token character varying(128) NOT NULL COLLATE pg_catalog."C"
);
CREATE TABLE "event-management".event_management_migrations (
 id character varying(255) NOT NULL
);
CREATE TABLE "event-management".events (
 tenant_id character varying(128) NOT NULL,
 device_token character varying(128) NOT NULL COLLATE pg_catalog."C",
 event_type bigint NOT NULL,
 occurred_time timestamp with time zone NOT NULL,
 source text,
 alt_id text,
 processed_time timestamp with time zone
);
CREATE TABLE "event-management".location_events (
 tenant_id character varying(128) NOT NULL,
 device_token character varying(128) NOT NULL COLLATE pg_catalog."C",
 event_type bigint NOT NULL,
 occurred_time timestamp with time zone NOT NULL,
 latitude numeric(10,8),
 longitude numeric(11,8),
 elevation numeric(12,4)
);
CREATE TABLE "event-management".measurement_events (
 tenant_id character varying(128) NOT NULL,
 device_token character varying(128) NOT NULL COLLATE pg_catalog."C",
 event_type bigint NOT NULL,
 occurred_time timestamp with time zone NOT NULL,
 name text NOT NULL,
 value numeric(20,8),
 classifier bigint,
 unit text,
 data_type character varying(32)
);
CREATE TABLE "event-management".state_change_events (
 tenant_id character varying(128) NOT NULL,
 device_token character varying(128) NOT NULL COLLATE pg_catalog."C",
 event_type bigint NOT NULL,
 occurred_time timestamp with time zone NOT NULL,
 state character varying(16) NOT NULL,
 reason text,
 session_id bigint DEFAULT 0 NOT NULL
);
CREATE UNIQUE INDEX idx_events_tenant_alt_id ON "event-management".events USING btree (tenant_id, alt_id, occurred_time) WHERE (alt_id IS NOT NULL);
CREATE UNIQUE INDEX uq_state_change_events_idem ON "event-management".state_change_events USING btree (tenant_id, device_token, occurred_time, state, session_id);
CREATE VIEW "event-management".measurement_rollups AS
 SELECT _materialized_hypertable_N.tenant_id,
 _materialized_hypertable_N.device_token,
 _materialized_hypertable_N.event_type,
 _materialized_hypertable_N.name,
 _materialized_hypertable_N.bucket,
 _materialized_hypertable_N.sum_value,
 _materialized_hypertable_N.min_value,
 _materialized_hypertable_N.max_value,
 _materialized_hypertable_N.count_value
 FROM _timescaledb_internal._materialized_hypertable_N
 WHERE (_materialized_hypertable_N.bucket < COALESCE(_timescaledb_functions.to_timestamp(_timescaledb_functions.cagg_watermark(N)), '-infinity'::timestamp with time zone))
UNION ALL
 SELECT measurement_events.tenant_id,
 measurement_events.device_token,
 measurement_events.event_type,
 measurement_events.name,
 public.time_bucket('00:01:00'::interval, measurement_events.occurred_time) AS bucket,
 sum(measurement_events.value) AS sum_value,
 min(measurement_events.value) AS min_value,
 max(measurement_events.value) AS max_value,
 count(measurement_events.value) AS count_value
 FROM "event-management".measurement_events
 WHERE (measurement_events.occurred_time >= COALESCE(_timescaledb_functions.to_timestamp(_timescaledb_functions.cagg_watermark(N)), '-infinity'::timestamp with time zone))
 GROUP BY measurement_events.tenant_id, measurement_events.device_token, measurement_events.event_type, measurement_events.name, (public.time_bucket('00:01:00'::interval, measurement_events.occurred_time));

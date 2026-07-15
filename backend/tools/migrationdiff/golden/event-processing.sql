ALTER SEQUENCE "event-processing".audit_events_id_seq OWNED BY "event-processing".audit_events.id;
ALTER TABLE ONLY "event-processing".audit_events
 ADD CONSTRAINT audit_events_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "event-processing".audit_events ALTER COLUMN id SET DEFAULT nextval('"event-processing".audit_events_id_seq'::regclass);
ALTER TABLE ONLY "event-processing".detect_rules
 ADD CONSTRAINT detect_rules_pkey PRIMARY KEY (rule_id);
ALTER TABLE ONLY "event-processing".detect_snapshots
 ADD CONSTRAINT detect_snapshots_pkey PRIMARY KEY (partition_id);
ALTER TABLE ONLY "event-processing".device_attribute_deletions
 ADD CONSTRAINT device_attribute_deletions_pkey PRIMARY KEY (tenant, device_token);
ALTER TABLE ONLY "event-processing".device_attributes
 ADD CONSTRAINT device_attributes_pkey PRIMARY KEY (tenant, device_token, scope, attr_key);
ALTER TABLE ONLY "event-processing".device_rosters
 ADD CONSTRAINT device_rosters_pkey PRIMARY KEY (tenant, device_token);
ALTER TABLE ONLY "event-processing".event_processing_migrations
 ADD CONSTRAINT event_processing_migrations_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "event-processing".profile_actives
 ADD CONSTRAINT profile_actives_pkey PRIMARY KEY (tenant, profile_token);
ALTER TABLE ONLY "event-processing".rule_stats
 ADD CONSTRAINT rule_stats_pkey PRIMARY KEY (rule_id);
CREATE INDEX "idx_event-processing_detect_rules_tenant" ON "event-processing".detect_rules USING btree (tenant);
CREATE INDEX "idx_event-processing_device_rosters_profile_token" ON "event-processing".device_rosters USING btree (profile_token);
CREATE INDEX "idx_event-processing_rule_stats_tenant" ON "event-processing".rule_stats USING btree (tenant);
CREATE INDEX idx_audit_tenant_time ON "event-processing".audit_events USING btree (tenant_id, occurred_time DESC);
CREATE SCHEMA "event-processing";
CREATE SEQUENCE "event-processing".audit_events_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE TABLE "event-processing".audit_events (
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
CREATE TABLE "event-processing".detect_rules (
 rule_id character varying(512) NOT NULL,
 tenant character varying(256) NOT NULL,
 profile_version_token character varying(256) NOT NULL,
 rule_token character varying(256) NOT NULL,
 definition text NOT NULL,
 updated_at timestamp with time zone,
 entity_group_token character varying(256) DEFAULT ''::character varying NOT NULL,
 entity_group_version integer DEFAULT 0 NOT NULL
);
CREATE TABLE "event-processing".detect_snapshots (
 partition_id character varying(128) NOT NULL,
 stream_seq bigint NOT NULL,
 watermark timestamp with time zone NOT NULL,
 payload bytea NOT NULL,
 updated_at timestamp with time zone
);
CREATE TABLE "event-processing".device_attribute_deletions (
 tenant character varying(256) NOT NULL,
 device_token character varying(256) NOT NULL,
 deleted_at timestamp with time zone NOT NULL,
 updated_at timestamp with time zone
);
CREATE TABLE "event-processing".device_attributes (
 tenant character varying(256) NOT NULL,
 device_token character varying(256) NOT NULL,
 scope character varying(64) NOT NULL,
 attr_key character varying(256) NOT NULL,
 value numeric NOT NULL,
 deleted boolean NOT NULL,
 last_event_at timestamp with time zone NOT NULL,
 updated_at timestamp with time zone
);
CREATE TABLE "event-processing".device_rosters (
 tenant character varying(256) NOT NULL,
 device_token character varying(256) NOT NULL,
 profile_token character varying(256) NOT NULL,
 expected_since timestamp with time zone NOT NULL,
 deleted boolean NOT NULL,
 last_event_at timestamp with time zone NOT NULL,
 updated_at timestamp with time zone
);
CREATE TABLE "event-processing".event_processing_migrations (
 id character varying(255) NOT NULL
);
CREATE TABLE "event-processing".profile_actives (
 tenant character varying(256) NOT NULL,
 profile_token character varying(256) NOT NULL,
 active_version_token character varying(256) NOT NULL,
 published_at timestamp with time zone NOT NULL,
 updated_at timestamp with time zone
);
CREATE TABLE "event-processing".rule_stats (
 rule_id character varying(512) NOT NULL,
 tenant character varying(256) NOT NULL,
 last_fired_at timestamp with time zone NOT NULL,
 fire_count bigint NOT NULL,
 last_edge character varying(16) NOT NULL
);

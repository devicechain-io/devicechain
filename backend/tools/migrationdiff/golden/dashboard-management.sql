ALTER SEQUENCE "dashboard-management".audit_events_id_seq OWNED BY "dashboard-management".audit_events.id;
ALTER SEQUENCE "dashboard-management".dashboard_versions_id_seq OWNED BY "dashboard-management".dashboard_versions.id;
ALTER SEQUENCE "dashboard-management".dashboards_id_seq OWNED BY "dashboard-management".dashboards.id;
ALTER TABLE ONLY "dashboard-management".audit_events
 ADD CONSTRAINT audit_events_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "dashboard-management".audit_events ALTER COLUMN id SET DEFAULT nextval('"dashboard-management".audit_events_id_seq'::regclass);
ALTER TABLE ONLY "dashboard-management".dashboard_management_migrations
 ADD CONSTRAINT dashboard_management_migrations_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "dashboard-management".dashboard_versions
 ADD CONSTRAINT dashboard_versions_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "dashboard-management".dashboard_versions ALTER COLUMN id SET DEFAULT nextval('"dashboard-management".dashboard_versions_id_seq'::regclass);
ALTER TABLE ONLY "dashboard-management".dashboards
 ADD CONSTRAINT dashboards_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "dashboard-management".dashboards ALTER COLUMN id SET DEFAULT nextval('"dashboard-management".dashboards_id_seq'::regclass);
CREATE INDEX "idx_dashboard-management_dashboard_versions_deleted_at" ON "dashboard-management".dashboard_versions USING btree (deleted_at);
CREATE INDEX "idx_dashboard-management_dashboard_versions_tenant_id" ON "dashboard-management".dashboard_versions USING btree (tenant_id);
CREATE INDEX "idx_dashboard-management_dashboards_deleted_at" ON "dashboard-management".dashboards USING btree (deleted_at);
CREATE INDEX "idx_dashboard-management_dashboards_tenant_id" ON "dashboard-management".dashboards USING btree (tenant_id);
CREATE INDEX "idx_dashboard-management_dashboards_token" ON "dashboard-management".dashboards USING btree (token);
CREATE INDEX idx_audit_tenant_time ON "dashboard-management".audit_events USING btree (tenant_id, occurred_time DESC);
CREATE SCHEMA "dashboard-management";
CREATE SEQUENCE "dashboard-management".audit_events_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "dashboard-management".dashboard_versions_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "dashboard-management".dashboards_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE TABLE "dashboard-management".audit_events (
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
CREATE TABLE "dashboard-management".dashboard_management_migrations (
 id character varying(255) NOT NULL
);
CREATE TABLE "dashboard-management".dashboard_versions (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 dashboard_id bigint NOT NULL,
 version integer NOT NULL,
 label character varying(128),
 description character varying(1024),
 definition jsonb NOT NULL,
 published_by character varying(256)
);
CREATE TABLE "dashboard-management".dashboards (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 name character varying(128),
 description character varying(1024),
 definition jsonb NOT NULL
);
CREATE UNIQUE INDEX uix_dashboard_versions_dashboard_version ON "dashboard-management".dashboard_versions USING btree (dashboard_id, version);
CREATE UNIQUE INDEX uix_dashboards_tenant_token ON "dashboard-management".dashboards USING btree (tenant_id, token) WHERE (deleted_at IS NULL);

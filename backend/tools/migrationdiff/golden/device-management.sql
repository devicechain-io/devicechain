ALTER SEQUENCE "device-management".alarms_id_seq OWNED BY "device-management".alarms.id;
ALTER SEQUENCE "device-management".area_types_id_seq OWNED BY "device-management".area_types.id;
ALTER SEQUENCE "device-management".areas_id_seq OWNED BY "device-management".areas.id;
ALTER SEQUENCE "device-management".asset_types_id_seq OWNED BY "device-management".asset_types.id;
ALTER SEQUENCE "device-management".assets_id_seq OWNED BY "device-management".assets.id;
ALTER SEQUENCE "device-management".audit_events_id_seq OWNED BY "device-management".audit_events.id;
ALTER SEQUENCE "device-management".command_definitions_id_seq OWNED BY "device-management".command_definitions.id;
ALTER SEQUENCE "device-management".customer_types_id_seq OWNED BY "device-management".customer_types.id;
ALTER SEQUENCE "device-management".customers_id_seq OWNED BY "device-management".customers.id;
ALTER SEQUENCE "device-management".detection_rule_scope_refs_id_seq OWNED BY "device-management".detection_rule_scope_refs.id;
ALTER SEQUENCE "device-management".detection_rules_id_seq OWNED BY "device-management".detection_rules.id;
ALTER SEQUENCE "device-management".device_claims_id_seq OWNED BY "device-management".device_claims.id;
ALTER SEQUENCE "device-management".device_credentials_id_seq OWNED BY "device-management".device_credentials.id;
ALTER SEQUENCE "device-management".device_profile_versions_id_seq OWNED BY "device-management".device_profile_versions.id;
ALTER SEQUENCE "device-management".device_profiles_id_seq OWNED BY "device-management".device_profiles.id;
ALTER SEQUENCE "device-management".device_types_id_seq OWNED BY "device-management".device_types.id;
ALTER SEQUENCE "device-management".devices_id_seq OWNED BY "device-management".devices.id;
ALTER SEQUENCE "device-management".entity_attributes_id_seq OWNED BY "device-management".entity_attributes.id;
ALTER SEQUENCE "device-management".entity_group_facet_refs_id_seq OWNED BY "device-management".entity_group_facet_refs.id;
ALTER SEQUENCE "device-management".entity_group_memberships_id_seq OWNED BY "device-management".entity_group_memberships.id;
ALTER SEQUENCE "device-management".entity_group_versions_id_seq OWNED BY "device-management".entity_group_versions.id;
ALTER SEQUENCE "device-management".entity_groups_id_seq OWNED BY "device-management".entity_groups.id;
ALTER SEQUENCE "device-management".entity_relationship_types_id_seq OWNED BY "device-management".entity_relationship_types.id;
ALTER SEQUENCE "device-management".entity_relationships_id_seq OWNED BY "device-management".entity_relationships.id;
ALTER SEQUENCE "device-management".facet_keys_id_seq OWNED BY "device-management".facet_keys.id;
ALTER SEQUENCE "device-management".metric_definitions_id_seq OWNED BY "device-management".metric_definitions.id;
ALTER SEQUENCE "device-management".provisioning_profiles_id_seq OWNED BY "device-management".provisioning_profiles.id;
ALTER TABLE ONLY "device-management".alarms
 ADD CONSTRAINT alarms_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".alarms ALTER COLUMN id SET DEFAULT nextval('"device-management".alarms_id_seq'::regclass);
ALTER TABLE ONLY "device-management".area_types
 ADD CONSTRAINT area_types_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".area_types ALTER COLUMN id SET DEFAULT nextval('"device-management".area_types_id_seq'::regclass);
ALTER TABLE ONLY "device-management".areas
 ADD CONSTRAINT "fk_device-management_area_types_areas" FOREIGN KEY (area_type_id) REFERENCES "device-management".area_types(id);
ALTER TABLE ONLY "device-management".areas
 ADD CONSTRAINT areas_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".areas ALTER COLUMN id SET DEFAULT nextval('"device-management".areas_id_seq'::regclass);
ALTER TABLE ONLY "device-management".asset_types
 ADD CONSTRAINT asset_types_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".asset_types ALTER COLUMN id SET DEFAULT nextval('"device-management".asset_types_id_seq'::regclass);
ALTER TABLE ONLY "device-management".assets
 ADD CONSTRAINT "fk_device-management_asset_types_assets" FOREIGN KEY (asset_type_id) REFERENCES "device-management".asset_types(id);
ALTER TABLE ONLY "device-management".assets
 ADD CONSTRAINT assets_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".assets ALTER COLUMN id SET DEFAULT nextval('"device-management".assets_id_seq'::regclass);
ALTER TABLE ONLY "device-management".audit_events
 ADD CONSTRAINT audit_events_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".audit_events ALTER COLUMN id SET DEFAULT nextval('"device-management".audit_events_id_seq'::regclass);
ALTER TABLE ONLY "device-management".command_definitions
 ADD CONSTRAINT command_definitions_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".command_definitions ALTER COLUMN id SET DEFAULT nextval('"device-management".command_definitions_id_seq'::regclass);
ALTER TABLE ONLY "device-management".customer_types
 ADD CONSTRAINT customer_types_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".customer_types ALTER COLUMN id SET DEFAULT nextval('"device-management".customer_types_id_seq'::regclass);
ALTER TABLE ONLY "device-management".customers
 ADD CONSTRAINT "fk_device-management_customer_types_customers" FOREIGN KEY (customer_type_id) REFERENCES "device-management".customer_types(id);
ALTER TABLE ONLY "device-management".customers
 ADD CONSTRAINT customers_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".customers ALTER COLUMN id SET DEFAULT nextval('"device-management".customers_id_seq'::regclass);
ALTER TABLE ONLY "device-management".detection_rule_scope_refs
 ADD CONSTRAINT detection_rule_scope_refs_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".detection_rule_scope_refs ALTER COLUMN id SET DEFAULT nextval('"device-management".detection_rule_scope_refs_id_seq'::regclass);
ALTER TABLE ONLY "device-management".detection_rules
 ADD CONSTRAINT detection_rules_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".detection_rules ALTER COLUMN id SET DEFAULT nextval('"device-management".detection_rules_id_seq'::regclass);
ALTER TABLE ONLY "device-management".device_claims
 ADD CONSTRAINT device_claims_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".device_claims ALTER COLUMN id SET DEFAULT nextval('"device-management".device_claims_id_seq'::regclass);
ALTER TABLE ONLY "device-management".device_credentials
 ADD CONSTRAINT "fk_device-management_device_credentials_device" FOREIGN KEY (device_id) REFERENCES "device-management".devices(id);
ALTER TABLE ONLY "device-management".device_credentials
 ADD CONSTRAINT device_credentials_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".device_credentials ALTER COLUMN id SET DEFAULT nextval('"device-management".device_credentials_id_seq'::regclass);
ALTER TABLE ONLY "device-management".device_management_migrations
 ADD CONSTRAINT device_management_migrations_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".device_profile_versions
 ADD CONSTRAINT device_profile_versions_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".device_profile_versions ALTER COLUMN id SET DEFAULT nextval('"device-management".device_profile_versions_id_seq'::regclass);
ALTER TABLE ONLY "device-management".device_profiles
 ADD CONSTRAINT device_profiles_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".device_profiles ALTER COLUMN id SET DEFAULT nextval('"device-management".device_profiles_id_seq'::regclass);
ALTER TABLE ONLY "device-management".device_types
 ADD CONSTRAINT device_types_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".device_types ALTER COLUMN id SET DEFAULT nextval('"device-management".device_types_id_seq'::regclass);
ALTER TABLE ONLY "device-management".devices
 ADD CONSTRAINT "fk_device-management_device_types_devices" FOREIGN KEY (device_type_id) REFERENCES "device-management".device_types(id);
ALTER TABLE ONLY "device-management".devices
 ADD CONSTRAINT devices_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".devices ALTER COLUMN id SET DEFAULT nextval('"device-management".devices_id_seq'::regclass);
ALTER TABLE ONLY "device-management".entity_attributes
 ADD CONSTRAINT entity_attributes_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".entity_attributes ALTER COLUMN id SET DEFAULT nextval('"device-management".entity_attributes_id_seq'::regclass);
ALTER TABLE ONLY "device-management".entity_group_facet_refs
 ADD CONSTRAINT entity_group_facet_refs_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".entity_group_facet_refs ALTER COLUMN id SET DEFAULT nextval('"device-management".entity_group_facet_refs_id_seq'::regclass);
ALTER TABLE ONLY "device-management".entity_group_memberships
 ADD CONSTRAINT entity_group_memberships_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".entity_group_memberships ALTER COLUMN id SET DEFAULT nextval('"device-management".entity_group_memberships_id_seq'::regclass);
ALTER TABLE ONLY "device-management".entity_group_versions
 ADD CONSTRAINT entity_group_versions_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".entity_group_versions ALTER COLUMN id SET DEFAULT nextval('"device-management".entity_group_versions_id_seq'::regclass);
ALTER TABLE ONLY "device-management".entity_groups
 ADD CONSTRAINT entity_groups_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".entity_groups ALTER COLUMN id SET DEFAULT nextval('"device-management".entity_groups_id_seq'::regclass);
ALTER TABLE ONLY "device-management".entity_relationship_types
 ADD CONSTRAINT entity_relationship_types_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".entity_relationship_types ALTER COLUMN id SET DEFAULT nextval('"device-management".entity_relationship_types_id_seq'::regclass);
ALTER TABLE ONLY "device-management".entity_relationships
 ADD CONSTRAINT "fk_device-management_entity_relationships_relationship_type" FOREIGN KEY (relationship_type_id) REFERENCES "device-management".entity_relationship_types(id);
ALTER TABLE ONLY "device-management".entity_relationships
 ADD CONSTRAINT entity_relationships_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".entity_relationships ALTER COLUMN id SET DEFAULT nextval('"device-management".entity_relationships_id_seq'::regclass);
ALTER TABLE ONLY "device-management".facet_keys
 ADD CONSTRAINT facet_keys_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".facet_keys ALTER COLUMN id SET DEFAULT nextval('"device-management".facet_keys_id_seq'::regclass);
ALTER TABLE ONLY "device-management".metric_definitions
 ADD CONSTRAINT metric_definitions_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".metric_definitions ALTER COLUMN id SET DEFAULT nextval('"device-management".metric_definitions_id_seq'::regclass);
ALTER TABLE ONLY "device-management".provisioning_profiles
 ADD CONSTRAINT "uni_device-management_provisioning_profiles_provision_key" UNIQUE (provision_key);
ALTER TABLE ONLY "device-management".provisioning_profiles
 ADD CONSTRAINT provisioning_profiles_pkey PRIMARY KEY (id);
ALTER TABLE ONLY "device-management".provisioning_profiles ALTER COLUMN id SET DEFAULT nextval('"device-management".provisioning_profiles_id_seq'::regclass);
CREATE INDEX "idx_device-management_alarms_deleted_at" ON "device-management".alarms USING btree (deleted_at);
CREATE INDEX "idx_device-management_alarms_tenant_id" ON "device-management".alarms USING btree (tenant_id);
CREATE INDEX "idx_device-management_alarms_token" ON "device-management".alarms USING btree (token);
CREATE INDEX "idx_device-management_area_types_deleted_at" ON "device-management".area_types USING btree (deleted_at);
CREATE INDEX "idx_device-management_area_types_tenant_id" ON "device-management".area_types USING btree (tenant_id);
CREATE INDEX "idx_device-management_area_types_token" ON "device-management".area_types USING btree (token);
CREATE INDEX "idx_device-management_areas_deleted_at" ON "device-management".areas USING btree (deleted_at);
CREATE INDEX "idx_device-management_areas_tenant_id" ON "device-management".areas USING btree (tenant_id);
CREATE INDEX "idx_device-management_areas_token" ON "device-management".areas USING btree (token);
CREATE INDEX "idx_device-management_asset_types_deleted_at" ON "device-management".asset_types USING btree (deleted_at);
CREATE INDEX "idx_device-management_asset_types_tenant_id" ON "device-management".asset_types USING btree (tenant_id);
CREATE INDEX "idx_device-management_asset_types_token" ON "device-management".asset_types USING btree (token);
CREATE INDEX "idx_device-management_assets_deleted_at" ON "device-management".assets USING btree (deleted_at);
CREATE INDEX "idx_device-management_assets_tenant_id" ON "device-management".assets USING btree (tenant_id);
CREATE INDEX "idx_device-management_assets_token" ON "device-management".assets USING btree (token);
CREATE INDEX "idx_device-management_command_definitions_deleted_at" ON "device-management".command_definitions USING btree (deleted_at);
CREATE INDEX "idx_device-management_command_definitions_tenant_id" ON "device-management".command_definitions USING btree (tenant_id);
CREATE INDEX "idx_device-management_command_definitions_token" ON "device-management".command_definitions USING btree (token);
CREATE INDEX "idx_device-management_customer_types_deleted_at" ON "device-management".customer_types USING btree (deleted_at);
CREATE INDEX "idx_device-management_customer_types_tenant_id" ON "device-management".customer_types USING btree (tenant_id);
CREATE INDEX "idx_device-management_customer_types_token" ON "device-management".customer_types USING btree (token);
CREATE INDEX "idx_device-management_customers_deleted_at" ON "device-management".customers USING btree (deleted_at);
CREATE INDEX "idx_device-management_customers_tenant_id" ON "device-management".customers USING btree (tenant_id);
CREATE INDEX "idx_device-management_customers_token" ON "device-management".customers USING btree (token);
CREATE INDEX "idx_device-management_detection_rule_scope_refs_deleted_at" ON "device-management".detection_rule_scope_refs USING btree (deleted_at);
CREATE INDEX "idx_device-management_detection_rule_scope_refs_tenant_id" ON "device-management".detection_rule_scope_refs USING btree (tenant_id);
CREATE INDEX "idx_device-management_detection_rules_deleted_at" ON "device-management".detection_rules USING btree (deleted_at);
CREATE INDEX "idx_device-management_detection_rules_device_profile_id" ON "device-management".detection_rules USING btree (device_profile_id);
CREATE INDEX "idx_device-management_detection_rules_tenant_id" ON "device-management".detection_rules USING btree (tenant_id);
CREATE INDEX "idx_device-management_detection_rules_token" ON "device-management".detection_rules USING btree (token);
CREATE INDEX "idx_device-management_device_claims_deleted_at" ON "device-management".device_claims USING btree (deleted_at);
CREATE INDEX "idx_device-management_device_claims_tenant_id" ON "device-management".device_claims USING btree (tenant_id);
CREATE INDEX "idx_device-management_device_credentials_credential_type" ON "device-management".device_credentials USING btree (credential_type);
CREATE INDEX "idx_device-management_device_credentials_deleted_at" ON "device-management".device_credentials USING btree (deleted_at);
CREATE INDEX "idx_device-management_device_credentials_device_id" ON "device-management".device_credentials USING btree (device_id);
CREATE INDEX "idx_device-management_device_credentials_tenant_id" ON "device-management".device_credentials USING btree (tenant_id);
CREATE INDEX "idx_device-management_device_credentials_token" ON "device-management".device_credentials USING btree (token);
CREATE INDEX "idx_device-management_device_profile_versions_deleted_at" ON "device-management".device_profile_versions USING btree (deleted_at);
CREATE INDEX "idx_device-management_device_profile_versions_tenant_id" ON "device-management".device_profile_versions USING btree (tenant_id);
CREATE INDEX "idx_device-management_device_profiles_category" ON "device-management".device_profiles USING btree (category);
CREATE INDEX "idx_device-management_device_profiles_deleted_at" ON "device-management".device_profiles USING btree (deleted_at);
CREATE INDEX "idx_device-management_device_profiles_tenant_id" ON "device-management".device_profiles USING btree (tenant_id);
CREATE INDEX "idx_device-management_device_profiles_token" ON "device-management".device_profiles USING btree (token);
CREATE INDEX "idx_device-management_device_types_deleted_at" ON "device-management".device_types USING btree (deleted_at);
CREATE INDEX "idx_device-management_device_types_manufacturer" ON "device-management".device_types USING btree (manufacturer);
CREATE INDEX "idx_device-management_device_types_model_name" ON "device-management".device_types USING btree (model);
CREATE INDEX "idx_device-management_device_types_profile_id" ON "device-management".device_types USING btree (profile_id);
CREATE INDEX "idx_device-management_device_types_tenant_id" ON "device-management".device_types USING btree (tenant_id);
CREATE INDEX "idx_device-management_device_types_token" ON "device-management".device_types USING btree (token);
CREATE INDEX "idx_device-management_devices_deleted_at" ON "device-management".devices USING btree (deleted_at);
CREATE INDEX "idx_device-management_devices_external_id" ON "device-management".devices USING btree (external_id);
CREATE INDEX "idx_device-management_devices_tenant_id" ON "device-management".devices USING btree (tenant_id);
CREATE INDEX "idx_device-management_devices_token" ON "device-management".devices USING btree (token);
CREATE INDEX "idx_device-management_entity_attributes_deleted_at" ON "device-management".entity_attributes USING btree (deleted_at);
CREATE INDEX "idx_device-management_entity_attributes_tenant_id" ON "device-management".entity_attributes USING btree (tenant_id);
CREATE INDEX "idx_device-management_entity_group_facet_refs_deleted_at" ON "device-management".entity_group_facet_refs USING btree (deleted_at);
CREATE INDEX "idx_device-management_entity_group_facet_refs_tenant_id" ON "device-management".entity_group_facet_refs USING btree (tenant_id);
CREATE INDEX "idx_device-management_entity_group_memberships_deleted_at" ON "device-management".entity_group_memberships USING btree (deleted_at);
CREATE INDEX "idx_device-management_entity_group_memberships_tenant_id" ON "device-management".entity_group_memberships USING btree (tenant_id);
CREATE INDEX "idx_device-management_entity_group_versions_deleted_at" ON "device-management".entity_group_versions USING btree (deleted_at);
CREATE INDEX "idx_device-management_entity_group_versions_tenant_id" ON "device-management".entity_group_versions USING btree (tenant_id);
CREATE INDEX "idx_device-management_entity_groups_deleted_at" ON "device-management".entity_groups USING btree (deleted_at);
CREATE INDEX "idx_device-management_entity_relationship_types_deleted_at" ON "device-management".entity_relationship_types USING btree (deleted_at);
CREATE INDEX "idx_device-management_entity_relationship_types_tenant_id" ON "device-management".entity_relationship_types USING btree (tenant_id);
CREATE INDEX "idx_device-management_entity_relationship_types_token" ON "device-management".entity_relationship_types USING btree (token);
CREATE INDEX "idx_device-management_entity_relationship_types_tracked" ON "device-management".entity_relationship_types USING btree (tracked);
CREATE INDEX "idx_device-management_entity_relationships_deleted_at" ON "device-management".entity_relationships USING btree (deleted_at);
CREATE INDEX "idx_device-management_entity_relationships_tenant_id" ON "device-management".entity_relationships USING btree (tenant_id);
CREATE INDEX "idx_device-management_entity_relationships_token" ON "device-management".entity_relationships USING btree (token);
CREATE INDEX "idx_device-management_metric_definitions_deleted_at" ON "device-management".metric_definitions USING btree (deleted_at);
CREATE INDEX "idx_device-management_metric_definitions_tenant_id" ON "device-management".metric_definitions USING btree (tenant_id);
CREATE INDEX "idx_device-management_metric_definitions_token" ON "device-management".metric_definitions USING btree (token);
CREATE INDEX "idx_device-management_provisioning_profiles_deleted_at" ON "device-management".provisioning_profiles USING btree (deleted_at);
CREATE INDEX "idx_device-management_provisioning_profiles_device_type_id" ON "device-management".provisioning_profiles USING btree (device_type_id);
CREATE INDEX "idx_device-management_provisioning_profiles_tenant_id" ON "device-management".provisioning_profiles USING btree (tenant_id);
CREATE INDEX "idx_device-management_provisioning_profiles_token" ON "device-management".provisioning_profiles USING btree (token);
CREATE INDEX idx_alarms_deleted_at ON "device-management".alarms USING btree (deleted_at);
CREATE INDEX idx_audit_tenant_time ON "device-management".audit_events USING btree (tenant_id, occurred_time DESC);
CREATE INDEX idx_dr_scope_ref_group ON "device-management".detection_rule_scope_refs USING btree (group_token, group_version);
CREATE INDEX idx_egfr_group ON "device-management".entity_group_facet_refs USING btree (group_id, selector_version);
CREATE INDEX idx_egfr_lookup ON "device-management".entity_group_facet_refs USING btree (facet_key, member_type);
CREATE INDEX idx_egm_entity ON "device-management".entity_group_memberships USING btree (entity_type, entity_id);
CREATE INDEX idx_egm_group ON "device-management".entity_group_memberships USING btree (group_id, selector_version);
CREATE INDEX idx_entity_attr_key ON "device-management".entity_attributes USING btree (entity_type, entity_id, scope, attr_key);
CREATE INDEX idx_entity_groups_deleted_at ON "device-management".entity_groups USING btree (deleted_at);
CREATE INDEX idx_entity_groups_member_type ON "device-management".entity_groups USING btree (member_type);
CREATE INDEX idx_entity_groups_tenant_id ON "device-management".entity_groups USING btree (tenant_id);
CREATE INDEX idx_entity_groups_token ON "device-management".entity_groups USING btree (token);
CREATE INDEX idx_entity_rel_source ON "device-management".entity_relationships USING btree (source_type, source_id);
CREATE INDEX idx_entity_rel_target ON "device-management".entity_relationships USING btree (target_type, target_id);
CREATE INDEX idx_facet_keys_deleted_at ON "device-management".facet_keys USING btree (deleted_at);
CREATE INDEX idx_facet_keys_member_type ON "device-management".facet_keys USING btree (member_type);
CREATE INDEX idx_facet_keys_tenant_id ON "device-management".facet_keys USING btree (tenant_id);
CREATE INDEX ix_entity_attributes_facet_lookup ON "device-management".entity_attributes USING btree (tenant_id, entity_type, scope, attr_key, entity_id) WHERE (deleted_at IS NULL);
CREATE INDEX ix_entity_attributes_facet_value ON "device-management".entity_attributes USING btree (tenant_id, entity_type, scope, attr_key, value) WHERE (deleted_at IS NULL);
CREATE SCHEMA "device-management";
CREATE SEQUENCE "device-management".alarms_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".area_types_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".areas_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".asset_types_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".assets_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".audit_events_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".command_definitions_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".customer_types_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".customers_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".detection_rule_scope_refs_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".detection_rules_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".device_claims_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".device_credentials_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".device_profile_versions_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".device_profiles_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".device_types_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".devices_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".entity_attributes_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".entity_group_facet_refs_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".entity_group_memberships_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".entity_group_versions_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".entity_groups_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".entity_relationship_types_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".entity_relationships_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".facet_keys_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".metric_definitions_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE "device-management".provisioning_profiles_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE TABLE "device-management".alarms (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 metadata jsonb,
 originator_type character varying(32) NOT NULL,
 originator_id bigint NOT NULL,
 alarm_key character varying(128) NOT NULL,
 metric_key character varying(128) NOT NULL,
 state character varying(16) NOT NULL,
 acknowledged boolean DEFAULT false NOT NULL,
 severity character varying(16) NOT NULL,
 raised_time timestamp with time zone NOT NULL,
 cleared_time timestamp with time zone,
 acknowledged_time timestamp with time zone,
 acknowledged_by character varying(256),
 last_value numeric,
 message character varying(1024),
 contributors jsonb,
 contributor_version bigint DEFAULT 0 NOT NULL
);
CREATE TABLE "device-management".area_types (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 name character varying(128),
 description character varying(1024),
 image_url character varying(512),
 icon character varying(128),
 background_color character varying(32),
 foreground_color character varying(32),
 border_color character varying(32),
 metadata jsonb
);
CREATE TABLE "device-management".areas (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 name character varying(128),
 description character varying(1024),
 metadata jsonb,
 area_type_id bigint
);
CREATE TABLE "device-management".asset_types (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 name character varying(128),
 description character varying(1024),
 image_url character varying(512),
 icon character varying(128),
 background_color character varying(32),
 foreground_color character varying(32),
 border_color character varying(32),
 metadata jsonb
);
CREATE TABLE "device-management".assets (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 name character varying(128),
 description character varying(1024),
 metadata jsonb,
 asset_type_id bigint
);
CREATE TABLE "device-management".audit_events (
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
CREATE TABLE "device-management".command_definitions (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 name character varying(128),
 description character varying(1024),
 metadata jsonb,
 command_key character varying(128) NOT NULL,
 parameter_schema jsonb,
 device_profile_id bigint NOT NULL
);
CREATE TABLE "device-management".customer_types (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 name character varying(128),
 description character varying(1024),
 image_url character varying(512),
 icon character varying(128),
 background_color character varying(32),
 foreground_color character varying(32),
 border_color character varying(32),
 metadata jsonb
);
CREATE TABLE "device-management".customers (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 name character varying(128),
 description character varying(1024),
 metadata jsonb,
 customer_type_id bigint
);
CREATE TABLE "device-management".detection_rule_scope_refs (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 device_profile_id bigint NOT NULL,
 rule_token character varying(255) NOT NULL,
 group_token character varying(255) NOT NULL,
 group_version integer NOT NULL
);
CREATE TABLE "device-management".detection_rules (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 name character varying(128),
 description character varying(1024),
 metadata jsonb,
 device_profile_id bigint NOT NULL,
 definition jsonb NOT NULL,
 enabled boolean DEFAULT true NOT NULL,
 authoring_graph jsonb,
 entity_group_token text,
 entity_group_version integer
);
CREATE TABLE "device-management".device_claims (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 device_id bigint NOT NULL,
 claim_secret character varying(256) NOT NULL,
 status character varying(32) NOT NULL,
 expires_at timestamp with time zone,
 claimed_time timestamp with time zone,
 claimed_by_customer_id bigint
);
CREATE TABLE "device-management".device_credentials (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 metadata jsonb,
 device_id bigint NOT NULL,
 credential_type character varying(32) NOT NULL,
 credential_id character varying(256) NOT NULL,
 credential_value character varying(4096),
 enabled boolean DEFAULT true NOT NULL,
 expires_at timestamp with time zone
);
CREATE TABLE "device-management".device_management_migrations (
 id character varying(255) NOT NULL
);
CREATE TABLE "device-management".device_profile_versions (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 device_profile_id bigint NOT NULL,
 version integer NOT NULL,
 label character varying(128),
 description character varying(1024),
 snapshot jsonb NOT NULL,
 published_by character varying(256)
);
CREATE TABLE "device-management".device_profiles (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 name character varying(128),
 description character varying(1024),
 metadata jsonb,
 category character varying(64),
 provenance character varying(256),
 active_version integer
);
CREATE TABLE "device-management".device_types (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 name character varying(128),
 description character varying(1024),
 image_url character varying(512),
 icon character varying(128),
 background_color character varying(32),
 foreground_color character varying(32),
 border_color character varying(32),
 metadata jsonb,
 profile_id bigint,
 manufacturer character varying(128),
 model character varying(128)
);
CREATE TABLE "device-management".devices (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 name character varying(128),
 description character varying(1024),
 metadata jsonb,
 device_type_id bigint,
 external_id character varying(256)
);
CREATE TABLE "device-management".entity_attributes (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 entity_type character varying(32) NOT NULL,
 entity_id bigint NOT NULL,
 scope character varying(16) NOT NULL,
 attr_key character varying(256) NOT NULL,
 value_type character varying(16) NOT NULL,
 value character varying(65536),
 last_updated timestamp with time zone NOT NULL
);
CREATE TABLE "device-management".entity_group_facet_refs (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 facet_key character varying(256) NOT NULL,
 member_type character varying(32) NOT NULL,
 group_id bigint NOT NULL,
 selector_version integer NOT NULL,
 group_token character varying(255) NOT NULL
);
CREATE TABLE "device-management".entity_group_memberships (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 entity_type character varying(32) NOT NULL,
 entity_id bigint NOT NULL,
 group_id bigint NOT NULL,
 selector_version integer NOT NULL,
 group_token character varying(255) NOT NULL
);
CREATE TABLE "device-management".entity_group_versions (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 entity_group_id bigint NOT NULL,
 version integer NOT NULL,
 selector text NOT NULL,
 member_type character varying(32) NOT NULL,
 selector_schema bigint NOT NULL,
 label character varying(128),
 description character varying(1024),
 published_by character varying(256)
);
CREATE TABLE "device-management".entity_groups (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 name character varying(128),
 description character varying(1024),
 image_url character varying(512),
 icon character varying(128),
 background_color character varying(32),
 foreground_color character varying(32),
 border_color character varying(32),
 metadata jsonb,
 member_type character varying(32) NOT NULL,
 membership_mode character varying(16) NOT NULL,
 selector text,
 selector_schema bigint DEFAULT 0 NOT NULL,
 active_version integer
);
CREATE TABLE "device-management".entity_relationship_types (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 name character varying(128),
 description character varying(1024),
 metadata jsonb,
 tracked boolean DEFAULT false NOT NULL
);
CREATE TABLE "device-management".entity_relationships (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 metadata jsonb,
 source_type character varying(32) NOT NULL,
 source_id bigint NOT NULL,
 target_type character varying(32) NOT NULL,
 target_id bigint NOT NULL,
 relationship_type_id bigint NOT NULL,
 target_token character varying(128)
);
CREATE TABLE "device-management".facet_keys (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 member_type character varying(32) NOT NULL,
 attr_key character varying(128) NOT NULL,
 value_type character varying(16) NOT NULL,
 source character varying(16) DEFAULT 'attribute'::character varying NOT NULL,
 "values" jsonb,
 label text
);
CREATE TABLE "device-management".metric_definitions (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 name character varying(128),
 description character varying(1024),
 metadata jsonb,
 metric_key character varying(128) NOT NULL,
 data_type character varying(16) NOT NULL,
 unit text,
 min_value numeric,
 max_value numeric,
 enum jsonb,
 descriptor text,
 device_profile_id bigint NOT NULL
);
CREATE TABLE "device-management".provisioning_profiles (
 id bigint NOT NULL,
 created_at timestamp with time zone,
 updated_at timestamp with time zone,
 deleted_at timestamp with time zone,
 tenant_id character varying(128) NOT NULL,
 token character varying(128) NOT NULL,
 name character varying(128),
 description character varying(1024),
 metadata jsonb,
 provision_key character varying(256) NOT NULL,
 provision_secret character varying(256) NOT NULL,
 strategy character varying(32) NOT NULL,
 device_type_id bigint NOT NULL,
 credential_type character varying(32) NOT NULL,
 enabled boolean NOT NULL,
 expires_at timestamp with time zone
);
CREATE UNIQUE INDEX idx_command_definition_profile_key ON "device-management".command_definitions USING btree (device_profile_id, command_key);
CREATE UNIQUE INDEX idx_device_claim_device ON "device-management".device_claims USING btree (device_id);
CREATE UNIQUE INDEX idx_device_credential_lookup ON "device-management".device_credentials USING btree (tenant_id, credential_type, credential_id) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX idx_metric_definition_profile_key ON "device-management".metric_definitions USING btree (device_profile_id, metric_key);
CREATE UNIQUE INDEX uix_alarm_originator_key ON "device-management".alarms USING btree (tenant_id, originator_type, originator_id, alarm_key) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_alarms_tenant_token ON "device-management".alarms USING btree (tenant_id, token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_area_types_tenant_token ON "device-management".area_types USING btree (tenant_id, token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_areas_tenant_token ON "device-management".areas USING btree (tenant_id, token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_asset_types_tenant_token ON "device-management".asset_types USING btree (tenant_id, token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_assets_tenant_token ON "device-management".assets USING btree (tenant_id, token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_command_definitions_tenant_profile_key ON "device-management".command_definitions USING btree (tenant_id, device_profile_id, command_key) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_command_definitions_tenant_token ON "device-management".command_definitions USING btree (tenant_id, token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_customer_types_tenant_token ON "device-management".customer_types USING btree (tenant_id, token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_customers_tenant_token ON "device-management".customers USING btree (tenant_id, token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_detection_rules_tenant_token ON "device-management".detection_rules USING btree (tenant_id, token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_device_credentials_tenant_token ON "device-management".device_credentials USING btree (tenant_id, token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_device_profile_versions_profile_version ON "device-management".device_profile_versions USING btree (device_profile_id, version);
CREATE UNIQUE INDEX uix_device_profiles_tenant_token ON "device-management".device_profiles USING btree (tenant_id, token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_device_types_tenant_token ON "device-management".device_types USING btree (tenant_id, token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_devices_tenant_external_id ON "device-management".devices USING btree (tenant_id, external_id) WHERE ((deleted_at IS NULL) AND (external_id IS NOT NULL));
CREATE UNIQUE INDEX uix_devices_tenant_token ON "device-management".devices USING btree (tenant_id, token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_dr_scope_ref ON "device-management".detection_rule_scope_refs USING btree (device_profile_id, rule_token);
CREATE UNIQUE INDEX uix_entity_group_facet_ref ON "device-management".entity_group_facet_refs USING btree (group_id, selector_version, facet_key);
CREATE UNIQUE INDEX uix_entity_group_membership ON "device-management".entity_group_memberships USING btree (group_id, selector_version, entity_type, entity_id);
CREATE UNIQUE INDEX uix_entity_group_versions_group_version ON "device-management".entity_group_versions USING btree (entity_group_id, version);
CREATE UNIQUE INDEX uix_entity_groups_tenant_token ON "device-management".entity_groups USING btree (tenant_id, token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_entity_relationship_types_tenant_token ON "device-management".entity_relationship_types USING btree (tenant_id, token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_entity_relationships_tenant_token ON "device-management".entity_relationships USING btree (tenant_id, token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_facet_keys_tenant_member_key ON "device-management".facet_keys USING btree (tenant_id, member_type, attr_key) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_metric_definitions_tenant_token ON "device-management".metric_definitions USING btree (tenant_id, token) WHERE (deleted_at IS NULL);
CREATE UNIQUE INDEX uix_provisioning_profiles_tenant_token ON "device-management".provisioning_profiles USING btree (tenant_id, token) WHERE (deleted_at IS NULL);

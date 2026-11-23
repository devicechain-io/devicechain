ALTER SEQUENCE fleet.audit_events_id_seq OWNED BY fleet.audit_events.id;
ALTER SEQUENCE fleet.cloud_identities_id_seq OWNED BY fleet.cloud_identities.id;
ALTER SEQUENCE fleet.cloud_operations_id_seq OWNED BY fleet.cloud_operations.id;
ALTER SEQUENCE fleet.import_records_id_seq OWNED BY fleet.import_records.id;
ALTER SEQUENCE fleet.runs_id_seq OWNED BY fleet.runs.id;
ALTER SEQUENCE fleet.writer_slot_transitions_id_seq OWNED BY fleet.writer_slot_transitions.id;
ALTER TABLE ONLY fleet.audit_events
 ADD CONSTRAINT audit_events_pkey PRIMARY KEY (id);
ALTER TABLE ONLY fleet.audit_events ALTER COLUMN id SET DEFAULT nextval('fleet.audit_events_id_seq'::regclass);
ALTER TABLE ONLY fleet.cloud_identities
 ADD CONSTRAINT cloud_identities_pkey PRIMARY KEY (id);
ALTER TABLE ONLY fleet.cloud_identities ALTER COLUMN id SET DEFAULT nextval('fleet.cloud_identities_id_seq'::regclass);
ALTER TABLE ONLY fleet.cloud_operations
 ADD CONSTRAINT cloud_operations_pkey PRIMARY KEY (id);
ALTER TABLE ONLY fleet.cloud_operations
 ADD CONSTRAINT fk_cloud_operations_cluster_uid FOREIGN KEY (cluster_uid) REFERENCES fleet.cloud_identities(cluster_uid);
ALTER TABLE ONLY fleet.cloud_operations
 ADD CONSTRAINT fk_cloud_operations_run_id FOREIGN KEY (run_id) REFERENCES fleet.runs(id);
ALTER TABLE ONLY fleet.cloud_operations ALTER COLUMN id SET DEFAULT nextval('fleet.cloud_operations_id_seq'::regclass);
ALTER TABLE ONLY fleet.cluster_observations
 ADD CONSTRAINT cluster_observations_pkey PRIMARY KEY (cluster_uid);
ALTER TABLE ONLY fleet.cluster_observations
 ADD CONSTRAINT fk_cluster_observations_cluster_uid FOREIGN KEY (cluster_uid) REFERENCES fleet.cloud_identities(cluster_uid);
ALTER TABLE ONLY fleet.fleet_migrations
 ADD CONSTRAINT fleet_migrations_pkey PRIMARY KEY (id);
ALTER TABLE ONLY fleet.import_records
 ADD CONSTRAINT fk_import_records_cluster_uid FOREIGN KEY (cluster_uid) REFERENCES fleet.cloud_identities(cluster_uid);
ALTER TABLE ONLY fleet.import_records
 ADD CONSTRAINT fk_import_records_run_id FOREIGN KEY (run_id) REFERENCES fleet.runs(id);
ALTER TABLE ONLY fleet.import_records
 ADD CONSTRAINT import_records_pkey PRIMARY KEY (id);
ALTER TABLE ONLY fleet.import_records ALTER COLUMN id SET DEFAULT nextval('fleet.import_records_id_seq'::regclass);
ALTER TABLE ONLY fleet.manager_lease
 ADD CONSTRAINT manager_lease_pkey PRIMARY KEY (id);
ALTER TABLE ONLY fleet.purged_tenants
 ADD CONSTRAINT purged_tenants_pkey PRIMARY KEY (token, epoch);
ALTER TABLE ONLY fleet.runs
 ADD CONSTRAINT fk_runs_cluster_uid FOREIGN KEY (cluster_uid) REFERENCES fleet.cloud_identities(cluster_uid);
ALTER TABLE ONLY fleet.runs
 ADD CONSTRAINT runs_pkey PRIMARY KEY (id);
ALTER TABLE ONLY fleet.runs ALTER COLUMN id SET DEFAULT nextval('fleet.runs_id_seq'::regclass);
ALTER TABLE ONLY fleet.state_heads
 ADD CONSTRAINT fk_state_heads_cluster_uid FOREIGN KEY (cluster_uid) REFERENCES fleet.cloud_identities(cluster_uid);
ALTER TABLE ONLY fleet.state_heads
 ADD CONSTRAINT state_heads_pkey PRIMARY KEY (cluster_uid);
ALTER TABLE ONLY fleet.writer_slot_transitions
 ADD CONSTRAINT fk_writer_slot_transitions_cluster_uid FOREIGN KEY (cluster_uid) REFERENCES fleet.cloud_identities(cluster_uid);
ALTER TABLE ONLY fleet.writer_slot_transitions
 ADD CONSTRAINT writer_slot_transitions_pkey PRIMARY KEY (id);
ALTER TABLE ONLY fleet.writer_slot_transitions ALTER COLUMN id SET DEFAULT nextval('fleet.writer_slot_transitions_id_seq'::regclass);
ALTER TABLE ONLY fleet.writer_slots
 ADD CONSTRAINT fk_writer_slots_cluster_uid FOREIGN KEY (cluster_uid) REFERENCES fleet.cloud_identities(cluster_uid);
ALTER TABLE ONLY fleet.writer_slots
 ADD CONSTRAINT writer_slots_pkey PRIMARY KEY (cluster_uid);
CREATE FUNCTION fleet.cloud_identities_immutable() RETURNS trigger
 LANGUAGE plpgsql
 AS $$
BEGIN
 IF NEW.cluster_uid IS DISTINCT FROM OLD.cluster_uid THEN
 RAISE EXCEPTION 'cloud_identities.cluster_uid is immutable' USING ERRCODE = 'integrity_constraint_violation';
CREATE FUNCTION fleet.writer_slot_transitions_append_only() RETURNS trigger
 LANGUAGE plpgsql
 AS $$
BEGIN
 RAISE EXCEPTION 'writer_slot_transitions is append-only' USING ERRCODE = 'integrity_constraint_violation';
CREATE INDEX idx_audit_tenant_time ON fleet.audit_events USING btree (tenant_id, occurred_time DESC);
CREATE INDEX ix_cloud_identities_onboarding_id ON fleet.cloud_identities USING btree (onboarding_id);
CREATE INDEX ix_cloud_operations_unsettled ON fleet.cloud_operations USING btree (cluster_uid) WHERE (NOT ((status = 'DONE'::text) AND ((error_code IS NULL) OR COALESCE((resolution = ANY (ARRAY['RECONCILED'::text, 'QUARANTINED'::text])), false))));
CREATE INDEX ix_import_records_cluster_uid ON fleet.import_records USING btree (cluster_uid);
CREATE INDEX ix_writer_slot_transitions_cluster_uid ON fleet.writer_slot_transitions USING btree (cluster_uid);
CREATE SCHEMA fleet;
CREATE SEQUENCE fleet.audit_events_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE fleet.cloud_identities_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE fleet.cloud_operations_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE fleet.import_records_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE fleet.runs_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE SEQUENCE fleet.writer_slot_transitions_id_seq
 START WITH 1
 INCREMENT BY 1
 NO MINVALUE
 NO MAXVALUE
 CACHE 1;
CREATE TABLE fleet.audit_events (
 id bigint NOT NULL,
 occurred_time timestamp with time zone NOT NULL,
 tenant_id text,
 category text NOT NULL,
 actor text,
 table_name text,
 operation text NOT NULL,
 entity_pk text,
 entity_label text,
 rows_affected bigint,
 client_id text,
 session_id text,
 on_behalf_of text,
 proposal text
);
CREATE TABLE fleet.cloud_identities (
 id bigint NOT NULL,
 cluster_uid uuid NOT NULL,
 onboarding_id uuid NOT NULL,
 management_mode text NOT NULL,
 cloud text NOT NULL,
 project_id text NOT NULL,
 location text NOT NULL,
 cloud_name text NOT NULL,
 ownership_nonce text,
 state_backend text,
 state_ref text,
 data_class text NOT NULL,
 lifecycle text DEFAULT 'INTENT'::text NOT NULL,
 tombstoned_at timestamp with time zone,
 intent_recorded_at timestamp with time zone NOT NULL,
 first_submit_at timestamp with time zone,
 cloud_resource_id text,
 recovered_via text,
 recovered_at timestamp with time zone,
 created_at timestamp with time zone NOT NULL,
 updated_at timestamp with time zone NOT NULL,
 CONSTRAINT ck_cloud_identities_cloud_resource_id_nonempty_v1 CHECK (((cloud_resource_id IS NULL) OR (cloud_resource_id <> ''::text))),
 CONSTRAINT ck_cloud_identities_cloud_v1 CHECK ((cloud = 'gke'::text)),
 CONSTRAINT ck_cloud_identities_data_class_v1 CHECK ((data_class = ANY (ARRAY['disposable'::text, 'retained'::text]))),
 CONSTRAINT ck_cloud_identities_lifecycle_v1 CHECK ((lifecycle = ANY (ARRAY['INTENT'::text, 'ACTIVE'::text, 'TEARING_DOWN'::text, 'DESTROYED'::text]))),
 CONSTRAINT ck_cloud_identities_management_mode_v1 CHECK ((management_mode = ANY (ARRAY['managed'::text, 'observe_only'::text]))),
 CONSTRAINT ck_cloud_identities_mode_columns_v1 CHECK ((((management_mode = 'managed'::text) AND (ownership_nonce IS NOT NULL) AND (state_backend IS NOT NULL) AND (state_ref IS NOT NULL)) OR ((management_mode = 'observe_only'::text) AND (ownership_nonce IS NULL) AND (state_backend IS NULL) AND (state_ref IS NULL)))),
 CONSTRAINT ck_cloud_identities_nonce_shape_v1 CHECK (((ownership_nonce IS NULL) OR (ownership_nonce ~ '^[a-z2-7]{26}$'::text))),
 CONSTRAINT ck_cloud_identities_recovered_via_v1 CHECK (((recovered_via IS NULL) OR (recovered_via = ANY (ARRAY['apply'::text, 'import'::text])))),
 CONSTRAINT ck_cloud_identities_recovery_pair_v1 CHECK (((cloud_resource_id IS NULL) = (recovered_via IS NULL))),
 CONSTRAINT ck_cloud_identities_state_backend_v1 CHECK (((state_backend IS NULL) OR (state_backend = ANY (ARRAY['gcs'::text, 'pg'::text]))))
);
CREATE TABLE fleet.cloud_operations (
 id bigint NOT NULL,
 cluster_uid uuid NOT NULL,
 run_id bigint,
 operation_name text NOT NULL,
 operation_type text,
 target_link text,
 status text NOT NULL,
 error_code text,
 error_message text,
 resolution text,
 cloud_start_time timestamp with time zone,
 cloud_end_time timestamp with time zone,
 source text NOT NULL,
 first_seen_at timestamp with time zone NOT NULL,
 last_seen_at timestamp with time zone NOT NULL,
 CONSTRAINT ck_cloud_operations_resolution_v1 CHECK (((resolution IS NULL) OR (resolution = ANY (ARRAY['RECONCILED'::text, 'QUARANTINED'::text])))),
 CONSTRAINT ck_cloud_operations_source_v1 CHECK ((source = ANY (ARRAY['RUNNER_LOG'::text, 'OBSERVER_LIST'::text]))),
 CONSTRAINT ck_cloud_operations_status_v1 CHECK ((status = ANY (ARRAY['PENDING'::text, 'RUNNING'::text, 'DONE'::text, 'ABORTING'::text, 'UNKNOWN'::text])))
);
CREATE TABLE fleet.cluster_observations (
 cluster_uid uuid NOT NULL,
 observer_listed_at timestamp with time zone,
 observer_list_complete boolean DEFAULT false NOT NULL,
 observer_error text,
 cluster_status text,
 updated_at timestamp with time zone NOT NULL
);
CREATE TABLE fleet.fleet_migrations (
 id character varying(255) NOT NULL
);
CREATE TABLE fleet.import_records (
 id bigint NOT NULL,
 cluster_uid uuid NOT NULL,
 run_id bigint NOT NULL,
 address text NOT NULL,
 cloud_id text,
 evidence jsonb,
 decision text NOT NULL,
 reason text NOT NULL,
 actor_id text NOT NULL,
 actor_issuer text NOT NULL,
 request_id uuid NOT NULL,
 decided_at timestamp with time zone NOT NULL,
 CONSTRAINT ck_import_records_decision_v1 CHECK ((decision = ANY (ARRAY['IMPORTED'::text, 'REFUSED'::text]))),
 CONSTRAINT ck_import_records_reason_v1 CHECK ((reason = ANY (ARRAY['VERIFIED'::text, 'NAME_MISMATCH'::text, 'PROJECT_MISMATCH'::text, 'LOCATION_MISMATCH'::text, 'NONCE_MISMATCH'::text, 'CREATE_TIME_BEFORE_INTENT'::text, 'IDENTITY_MISMATCH'::text, 'POOL_PARENT_NOT_VERIFIED'::text, 'POOL_NOT_DECLARED'::text]))),
 CONSTRAINT ck_import_records_verdict_matches_reason_v1 CHECK ((((decision = 'IMPORTED'::text) AND (reason = 'VERIFIED'::text)) OR ((decision = 'REFUSED'::text) AND (reason <> 'VERIFIED'::text))))
);
CREATE TABLE fleet.manager_lease (
 id smallint NOT NULL,
 holder_id text NOT NULL,
 lease_epoch bigint DEFAULT 0 NOT NULL,
 expires_at timestamp with time zone NOT NULL,
 updated_at timestamp with time zone NOT NULL,
 CONSTRAINT ck_manager_lease_singleton_v1 CHECK ((id = 1))
);
CREATE TABLE fleet.purged_tenants (
 token character varying(128) NOT NULL,
 epoch timestamp with time zone NOT NULL,
 planted_at timestamp with time zone NOT NULL,
 completed_at timestamp with time zone
);
CREATE TABLE fleet.runs (
 id bigint NOT NULL,
 run_uid uuid NOT NULL,
 cluster_uid uuid NOT NULL,
 epoch bigint NOT NULL,
 request_id uuid NOT NULL,
 actor_id text NOT NULL,
 actor_issuer text NOT NULL,
 kind text NOT NULL,
 phase text NOT NULL,
 job_namespace text,
 job_uid text,
 pod_uid text,
 node_name text,
 credential_mode text NOT NULL,
 cloud_identity_role text NOT NULL,
 cloud_principal text NOT NULL,
 ksa_token_ttl_s bigint NOT NULL,
 sts_token_ttl_s bigint NOT NULL,
 access_token_ttl_s bigint NOT NULL,
 state_role_name text,
 runner_image_digest text,
 tofu_version text,
 provider_lock_hash text,
 module_digest text,
 manager_lease_epoch bigint NOT NULL,
 started_at timestamp with time zone,
 last_observed_at timestamp with time zone,
 ended_at timestamp with time zone,
 CONSTRAINT ck_runs_cloud_identity_role_v1 CHECK ((cloud_identity_role = ANY (ARRAY['apply'::text, 'destroy'::text]))),
 CONSTRAINT ck_runs_credential_mode_v1 CHECK ((credential_mode = ANY (ARRAY['kind_jwks'::text, 'gke_wi'::text]))),
 CONSTRAINT ck_runs_kind_v1 CHECK ((kind = ANY (ARRAY['CREATE'::text, 'CHANGE'::text, 'UPGRADE'::text, 'DRIFT_CHECK'::text, 'DESTROY'::text, 'RECOVER'::text]))),
 CONSTRAINT ck_runs_phase_v1 CHECK ((phase = ANY (ARRAY['INTENT'::text, 'JOB_CREATING'::text, 'RUNNING'::text, 'EXITED_OK'::text, 'EXITED_FAILED'::text, 'UNCERTAIN'::text, 'EXCLUDED'::text, 'RECONCILED'::text]))),
 CONSTRAINT ck_runs_token_ttls_positive_v1 CHECK (((ksa_token_ttl_s > 0) AND (sts_token_ttl_s > 0) AND (access_token_ttl_s > 0)))
);
CREATE TABLE fleet.state_heads (
 cluster_uid uuid NOT NULL,
 lineage text NOT NULL,
 serial bigint NOT NULL,
 recorded_run_uid uuid,
 recorded_at timestamp with time zone NOT NULL
);
CREATE TABLE fleet.writer_slot_transitions (
 id bigint NOT NULL,
 cluster_uid uuid NOT NULL,
 epoch bigint NOT NULL,
 run_uid uuid,
 from_state text,
 to_state text NOT NULL,
 cloud_cut_proof text,
 state_cut_method text,
 evidence jsonb,
 reason text,
 actor_id text NOT NULL,
 actor_issuer text NOT NULL,
 request_id uuid,
 manager_lease_epoch bigint NOT NULL,
 at timestamp with time zone NOT NULL,
 CONSTRAINT ck_writer_slot_transitions_from_state_v1 CHECK (((from_state IS NULL) OR (from_state = ANY (ARRAY['FREE'::text, 'HELD'::text, 'UNCERTAIN'::text, 'EXCLUDED'::text, 'RECOVERING'::text, 'QUARANTINED'::text])))),
 CONSTRAINT ck_writer_slot_transitions_proof_v1 CHECK (((cloud_cut_proof IS NULL) OR (cloud_cut_proof = ANY (ARRAY['TERMINATION'::text, 'EXPIRY'::text])))),
 CONSTRAINT ck_writer_slot_transitions_state_cut_v1 CHECK (((state_cut_method IS NULL) OR (state_cut_method = ANY (ARRAY['PROCESS_TERMINATED'::text, 'PG_ROLE_REVOKED'::text, 'CLOUD_CREDENTIAL'::text])))),
 CONSTRAINT ck_writer_slot_transitions_to_state_v1 CHECK ((to_state = ANY (ARRAY['FREE'::text, 'HELD'::text, 'UNCERTAIN'::text, 'EXCLUDED'::text, 'RECOVERING'::text, 'QUARANTINED'::text])))
);
CREATE TABLE fleet.writer_slots (
 cluster_uid uuid NOT NULL,
 epoch bigint DEFAULT 0 NOT NULL,
 state text DEFAULT 'FREE'::text NOT NULL,
 holder_run_uid uuid,
 doubted_from text,
 uncertain_since timestamp with time zone,
 refresh_cut_at timestamp with time zone,
 cloud_cut_proof text,
 cloud_cut_at timestamp with time zone,
 state_cut_method text,
 state_cut_at timestamp with time zone,
 containment text,
 containment_observed_at timestamp with time zone,
 quarantine_reason text,
 updated_at timestamp with time zone NOT NULL,
 CONSTRAINT ck_writer_slots_cloud_cut_proof_v1 CHECK (((cloud_cut_proof IS NULL) OR (cloud_cut_proof = ANY (ARRAY['TERMINATION'::text, 'EXPIRY'::text])))),
 CONSTRAINT ck_writer_slots_containment_v1 CHECK (((containment IS NULL) OR (containment = 'IDENTITY_DISABLED'::text))),
 CONSTRAINT ck_writer_slots_doubted_from_v1 CHECK (((doubted_from IS NULL) OR (doubted_from = ANY (ARRAY['HELD'::text, 'RECOVERING'::text])))),
 CONSTRAINT ck_writer_slots_excluded_both_cuts_v1 CHECK (((state <> 'EXCLUDED'::text) OR ((cloud_cut_proof IS NOT NULL) AND (cloud_cut_at IS NOT NULL) AND (state_cut_method IS NOT NULL) AND (state_cut_at IS NOT NULL)))),
 CONSTRAINT ck_writer_slots_expiry_v1 CHECK (((cloud_cut_proof IS DISTINCT FROM 'EXPIRY'::text) OR ((refresh_cut_at IS NOT NULL) AND (state_cut_method IS DISTINCT FROM 'PROCESS_TERMINATED'::text)))),
 CONSTRAINT ck_writer_slots_free_is_clear_v1 CHECK (((state <> 'FREE'::text) OR ((holder_run_uid IS NULL) AND (cloud_cut_proof IS NULL) AND (cloud_cut_at IS NULL) AND (state_cut_method IS NULL) AND (state_cut_at IS NULL) AND (refresh_cut_at IS NULL)))),
 CONSTRAINT ck_writer_slots_holder_v1 CHECK (((state <> ALL (ARRAY['HELD'::text, 'UNCERTAIN'::text, 'RECOVERING'::text])) OR (holder_run_uid IS NOT NULL))),
 CONSTRAINT ck_writer_slots_quarantine_has_reason_v1 CHECK (((state <> 'QUARANTINED'::text) OR (quarantine_reason IS NOT NULL))),
 CONSTRAINT ck_writer_slots_quarantine_reason_v1 CHECK (((quarantine_reason IS NULL) OR (quarantine_reason = ANY (ARRAY['NO_PROOF_OBTAINABLE'::text, 'IDENTITY_MISMATCH'::text, 'STATE_HEAD_MISMATCH'::text, 'FOREIGN_LOCK_HOLDER'::text, 'UNSETTLED_ERRORED_OPERATION'::text, 'SECOND_POD_FOR_RUN'::text, 'CREATE_TIME_BEFORE_INTENT'::text])))),
 CONSTRAINT ck_writer_slots_state_cut_method_v1 CHECK (((state_cut_method IS NULL) OR (state_cut_method = ANY (ARRAY['PROCESS_TERMINATED'::text, 'PG_ROLE_REVOKED'::text, 'CLOUD_CREDENTIAL'::text])))),
 CONSTRAINT ck_writer_slots_state_v1 CHECK ((state = ANY (ARRAY['FREE'::text, 'HELD'::text, 'UNCERTAIN'::text, 'EXCLUDED'::text, 'RECOVERING'::text, 'QUARANTINED'::text]))),
 CONSTRAINT ck_writer_slots_termination_v1 CHECK (((cloud_cut_proof IS DISTINCT FROM 'TERMINATION'::text) OR (state_cut_method = 'PROCESS_TERMINATED'::text))),
 CONSTRAINT ck_writer_slots_uncertain_v1 CHECK (((state <> 'UNCERTAIN'::text) OR (doubted_from IS NOT NULL)))
);
CREATE TRIGGER tr_cloud_identities_immutable BEFORE UPDATE ON fleet.cloud_identities FOR EACH ROW EXECUTE FUNCTION fleet.cloud_identities_immutable();
CREATE TRIGGER tr_writer_slot_transitions_append_only BEFORE DELETE OR UPDATE ON fleet.writer_slot_transitions FOR EACH ROW EXECUTE FUNCTION fleet.writer_slot_transitions_append_only();
CREATE TRIGGER tr_writer_slot_transitions_no_truncate BEFORE TRUNCATE ON fleet.writer_slot_transitions FOR EACH STATEMENT EXECUTE FUNCTION fleet.writer_slot_transitions_append_only();
CREATE UNIQUE INDEX ux_cloud_identities_cluster_uid ON fleet.cloud_identities USING btree (cluster_uid);
CREATE UNIQUE INDEX ux_cloud_identities_live_cloud_name ON fleet.cloud_identities USING btree (cloud, project_id, location, cloud_name) WHERE (lifecycle <> 'DESTROYED'::text);
CREATE UNIQUE INDEX ux_cloud_identities_ownership_nonce ON fleet.cloud_identities USING btree (ownership_nonce) WHERE (ownership_nonce IS NOT NULL);
CREATE UNIQUE INDEX ux_cloud_operations_cluster_uid_operation_name ON fleet.cloud_operations USING btree (cluster_uid, operation_name);
CREATE UNIQUE INDEX ux_import_records_imported_address ON fleet.import_records USING btree (cluster_uid, address) WHERE (decision = 'IMPORTED'::text);
CREATE UNIQUE INDEX ux_runs_cluster_uid_epoch ON fleet.runs USING btree (cluster_uid, epoch);
CREATE UNIQUE INDEX ux_runs_run_uid ON fleet.runs USING btree (run_uid);
END
$$;
END
$$;
END IF;
END IF;
END IF;
END IF;
END IF;
IF NEW.data_class IS DISTINCT FROM OLD.data_class THEN
 RAISE EXCEPTION 'cloud_identities.data_class is immutable' USING ERRCODE = 'integrity_constraint_violation';
IF NEW.management_mode IS DISTINCT FROM OLD.management_mode THEN
 RAISE EXCEPTION 'cloud_identities.management_mode is immutable' USING ERRCODE = 'integrity_constraint_violation';
IF OLD.cloud_resource_id IS NOT NULL AND NEW.cloud_resource_id IS DISTINCT FROM OLD.cloud_resource_id THEN
 RAISE EXCEPTION 'cloud_identities.cloud_resource_id is immutable once set' USING ERRCODE = 'integrity_constraint_violation';
IF OLD.ownership_nonce IS NOT NULL AND NEW.ownership_nonce IS DISTINCT FROM OLD.ownership_nonce THEN
 RAISE EXCEPTION 'cloud_identities.ownership_nonce is immutable once set' USING ERRCODE = 'integrity_constraint_violation';
RETURN NEW;

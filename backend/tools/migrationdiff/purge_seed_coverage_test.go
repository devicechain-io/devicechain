// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// seededLocation is one place seedTenant writes, with how many rows it puts there.
type seededLocation struct {
	label, table, where string
	rows                int64
}

// tenantRowCounts is every seeded location. The row count is carried per location rather
// than assumed to be one, so a location seeded with several rows cannot quietly pass an
// assertion written for a single one.
func tenantRowCounts() []seededLocation {
	return []seededLocation{
		{"device-management.asset_types", `"device-management".asset_types`, "tenant_id = ?", 1},
		{"device-management.asset_type_versions", `"device-management".asset_type_versions`, "tenant_id = ?", 1},
		{"device-management.entity_groups", `"device-management".entity_groups`, "tenant_id = ?", 1},
		{"device-management.entity_group_versions", `"device-management".entity_group_versions`, "tenant_id = ?", 1},
		{"device-management.geo_fence_set_versions", `"device-management".geo_fence_set_versions`, "tenant_id = ?", 1},
		{"dashboard-management.dashboards", `"dashboard-management".dashboards`, "tenant_id = ?", 1},
		{"dashboard-management.dashboard_versions", `"dashboard-management".dashboard_versions`, "tenant_id = ?", 1},
		{"outbound-connectors.connectors", `"outbound-connectors".connectors`, "tenant_id = ?", 1},
		{"outbound-connectors.connector_versions", `"outbound-connectors".connector_versions`, "tenant_id = ?", 1},
		{"user-management.iam_agent_policies", `"user-management".iam_agent_policies`, "tenant_id = ?", 1},
		{"user-management.iam_agent_policy_versions", `"user-management".iam_agent_policy_versions`, "tenant_id = ?", 1},

		{"device-management.area_types", `"device-management".area_types`, "tenant_id = ?", 1},
		{"device-management.areas", `"device-management".areas`, "tenant_id = ?", 1},
		{"device-management.devices", `"device-management".devices`, "tenant_id = ?", 1},
		{"device-management.device_credentials", `"device-management".device_credentials`, "tenant_id = ?", 1},
		{"device-management.device_profiles", `"device-management".device_profiles`, "tenant_id = ?", 1},
		{"device-management.device_profile_versions", `"device-management".device_profile_versions`, "tenant_id = ?", 1},
		{"device-management.device_types", `"device-management".device_types`, "tenant_id = ?", 1},
		{"device-management.automation_review_policies", `"device-management".automation_review_policies`, "tenant_id = ?", 1},
		{"device-management.automation_review_policy_changes", `"device-management".automation_review_policy_changes`, "tenant_id = ?", 1},
		{"device-management.automation_review_break_glass", `"device-management".automation_review_break_glass`, "tenant_id = ?", 1},
		{"event-processing.device_rosters", `"event-processing".device_rosters`, "tenant = ?", 1},
		{"user-management.iam_memberships", `"user-management".iam_memberships`, "tenant_id = ?", 1},
		{"event-management.measurement_events", `"event-management".measurement_events`, "tenant_id = ?", 3},
		{"event-management.failed_event_records", `"event-management".failed_event_records`, "tenant_id = ?", 2},
	}
}

// Drill exemptions never change what the purge erases. Keep the limits of the
// seeded proof explicit. New tables need a seeded assertion or an individually
// reviewed reason here. Published versions must always be seeded.
const (
	migrationTable             = "Migration bookkeeping has no tenant rows."
	purgeTombstone             = "Erasure tombstone intentionally survives; exercised by the purge fence tests."
	sharedJournal              = "Shared redaction mechanism; the drill asserts user-management and device-management journals separately."
	separateDrillAssertion     = "Seeded and asserted separately by the drill as shared identity/role or transitive join data."
	indirectTable              = "No direct tenant column; catalog classification and its exemption/FK rules remain the coverage gate."
	representedTenantPredicate = "Not seeded in this drill: ordinary tenant-column erasure is represented by devices; catalog classification remains enforced."
)

var purgeSeedExemptions = map[string]string{
	"device-state.dropped_measurements":                          representedTenantPredicate,
	"device-state.latest_measurements":                           representedTenantPredicate,
	"device-state.purged_tenants":                                purgeTombstone,
	"ai-inference.ai_function_assignments":                       representedTenantPredicate,
	"ai-inference.ai_inference_migrations":                       migrationTable,
	"ai-inference.ai_provider_tenant_grants":                     representedTenantPredicate,
	"ai-inference.ai_provider_tier_grants":                       indirectTable,
	"ai-inference.ai_providers":                                  indirectTable,
	"ai-inference.audit_events":                                  sharedJournal,
	"ai-inference.purged_tenants":                                purgeTombstone,
	"ai-inference.secrets":                                       representedTenantPredicate,
	"command-delivery.audit_events":                              sharedJournal,
	"command-delivery.campaign_cycles":                           representedTenantPredicate,
	"command-delivery.campaign_elements":                         representedTenantPredicate,
	"command-delivery.campaigns":                                 representedTenantPredicate,
	"command-delivery.command_batches":                           representedTenantPredicate,
	"command-delivery.command_delivery_migrations":               migrationTable,
	"command-delivery.commands":                                  representedTenantPredicate,
	"command-delivery.device_gate_syncs":                         representedTenantPredicate,
	"command-delivery.device_gates":                              representedTenantPredicate,
	"command-delivery.purged_tenants":                            purgeTombstone,
	"dashboard-management.audit_events":                          sharedJournal,
	"dashboard-management.dashboard_management_migrations":       migrationTable,
	"dashboard-management.dashboard_shares":                      representedTenantPredicate,
	"dashboard-management.purged_tenants":                        purgeTombstone,
	"device-management.agent_actuation_fences":                   representedTenantPredicate,
	"device-management.agent_effect_outboxes":                    representedTenantPredicate,
	"device-management.agent_proposals":                          representedTenantPredicate,
	"device-management.alarm_dispositions":                       representedTenantPredicate,
	"device-management.alarms":                                   representedTenantPredicate,
	"device-management.assets":                                   representedTenantPredicate,
	"device-management.audit_events":                             sharedJournal,
	"device-management.automation_reviews":                       representedTenantPredicate,
	"device-management.command_definitions":                      representedTenantPredicate,
	"device-management.customer_types":                           representedTenantPredicate,
	"device-management.customers":                                representedTenantPredicate,
	"device-management.detection_rule_scope_refs":                representedTenantPredicate,
	"device-management.detection_rules":                          representedTenantPredicate,
	"device-management.device_aliases":                           representedTenantPredicate,
	"device-management.device_claims":                            representedTenantPredicate,
	"device-management.device_configuration_revisions":           representedTenantPredicate,
	"device-management.device_configuration_states":              representedTenantPredicate,
	"device-management.device_credential_chain_links":            representedTenantPredicate,
	"device-management.device_management_migrations":             migrationTable,
	"device-management.device_profile_activations":               representedTenantPredicate,
	"device-management.device_replacements":                      representedTenantPredicate,
	"device-management.device_revocation_issuers":                representedTenantPredicate,
	"device-management.device_revocation_lists":                  representedTenantPredicate,
	"device-management.device_revoked_certificates":              representedTenantPredicate,
	"device-management.device_trust_anchors":                     representedTenantPredicate,
	"device-management.entity_attributes":                        representedTenantPredicate,
	"device-management.entity_group_facet_refs":                  representedTenantPredicate,
	"device-management.entity_group_memberships":                 representedTenantPredicate,
	"device-management.entity_relationship_types":                representedTenantPredicate,
	"device-management.entity_relationships":                     representedTenantPredicate,
	"device-management.facet_keys":                               representedTenantPredicate,
	"device-management.geo_fence_geometry_blobs":                 representedTenantPredicate,
	"device-management.geo_fences":                               representedTenantPredicate,
	"device-management.integration_device_types":                 representedTenantPredicate,
	"device-management.integration_unregistered_devices":         representedTenantPredicate,
	"device-management.integrations":                             representedTenantPredicate,
	"device-management.issued_customer_tokens":                   representedTenantPredicate,
	"device-management.issued_group_tokens":                      representedTenantPredicate,
	"device-management.maintenance_windows":                      representedTenantPredicate,
	"device-management.metric_definitions":                       representedTenantPredicate,
	"device-management.pending_ownership_facts":                  representedTenantPredicate,
	"device-management.pending_revocation_expansion_serials":     representedTenantPredicate,
	"device-management.pending_revocation_expansions":            representedTenantPredicate,
	"device-management.pending_revocation_kicks":                 representedTenantPredicate,
	"device-management.pending_session_kicks":                    representedTenantPredicate,
	"device-management.profile_decoder_healths":                  representedTenantPredicate,
	"device-management.profile_decoder_samples":                  representedTenantPredicate,
	"device-management.provisioning_profiles":                    representedTenantPredicate,
	"device-management.purged_tenants":                           purgeTombstone,
	"device-management.semantic_contexts":                        representedTenantPredicate,
	"device-state.audit_events":                                  sharedJournal,
	"device-state.device_state_migrations":                       migrationTable,
	"device-state.device_states":                                 representedTenantPredicate,
	"device-state.latest_locations":                              representedTenantPredicate,
	"event-management.alert_events":                              representedTenantPredicate,
	"event-management.audit_events":                              sharedJournal,
	"event-management.calculated_backfills":                      representedTenantPredicate,
	"event-management.event_anchors":                             representedTenantPredicate,
	"event-management.event_management_migrations":               migrationTable,
	"event-management.events":                                    representedTenantPredicate,
	"event-management.location_events":                           representedTenantPredicate,
	"event-management.purged_tenants":                            purgeTombstone,
	"event-management.state_change_events":                       representedTenantPredicate,
	"event-management.tenant_usage_daily":                        representedTenantPredicate,
	"event-management.tenant_usage_devices":                      representedTenantPredicate,
	"event-management.tenant_usage_hourly":                       representedTenantPredicate,
	"event-management.tenant_usage_ledger":                       indirectTable,
	"event-management.tenant_usage_state":                        indirectTable,
	"event-processing.audit_events":                              sharedJournal,
	"event-processing.detect_rules":                              representedTenantPredicate,
	"event-processing.detect_snapshots":                          indirectTable,
	"event-processing.device_attribute_deletions":                representedTenantPredicate,
	"event-processing.device_attributes":                         representedTenantPredicate,
	"event-processing.event_processing_migrations":               migrationTable,
	"event-processing.profile_actives":                           representedTenantPredicate,
	"event-processing.purged_tenants":                            purgeTombstone,
	"event-processing.rule_stats":                                representedTenantPredicate,
	"notification-management.audit_events":                       sharedJournal,
	"notification-management.notification_attempts":              representedTenantPredicate,
	"notification-management.notification_channels":              representedTenantPredicate,
	"notification-management.notification_deferrals":             representedTenantPredicate,
	"notification-management.notification_management_migrations": migrationTable,
	"notification-management.notification_policies":              representedTenantPredicate,
	"notification-management.notification_rules":                 representedTenantPredicate,
	"notification-management.notification_states":                representedTenantPredicate,
	"notification-management.purged_tenants":                     purgeTombstone,
	"notification-management.secrets":                            representedTenantPredicate,
	"outbound-connectors.audit_events":                           sharedJournal,
	"outbound-connectors.lpwan_pending_acks":                     representedTenantPredicate,
	"outbound-connectors.outbound_connectors_migrations":         migrationTable,
	"outbound-connectors.purged_tenants":                         purgeTombstone,
	"outbound-connectors.secrets":                                representedTenantPredicate,
	"update-management.artifact_signatures":                      representedTenantPredicate,
	"update-management.artifact_signers":                         representedTenantPredicate,
	"update-management.artifacts":                                representedTenantPredicate,
	"update-management.audit_events":                             sharedJournal,
	"update-management.purged_tenants":                           purgeTombstone,
	"update-management.secrets":                                  representedTenantPredicate,
	"update-management.update_assignments":                       representedTenantPredicate,
	"update-management.update_attempts":                          representedTenantPredicate,
	"update-management.update_management_migrations":             migrationTable,
	"update-management.update_rollout_targets":                   representedTenantPredicate,
	"update-management.update_rollouts":                          representedTenantPredicate,
	"user-management.audit_events":                               sharedJournal,
	"user-management.dead_letters":                               representedTenantPredicate,
	"user-management.iam_identities":                             separateDrillAssertion,
	"user-management.iam_identity_links":                         indirectTable,
	"user-management.iam_identity_system_roles":                  indirectTable,
	"user-management.iam_idp_connection_domains":                 indirectTable,
	"user-management.iam_idp_connection_tenants":                 representedTenantPredicate,
	"user-management.iam_idp_connections":                        indirectTable,
	"user-management.iam_idp_role_mappings":                      representedTenantPredicate,
	"user-management.iam_invitations":                            representedTenantPredicate,
	"user-management.iam_membership_tenant_roles":                separateDrillAssertion,
	"user-management.iam_mfa_factors":                            indirectTable,
	"user-management.iam_mfa_recovery_codes":                     indirectTable,
	"user-management.iam_mfa_source_failures":                    indirectTable,
	"user-management.iam_oauth_clients":                          indirectTable,
	"user-management.iam_partition_config":                       indirectTable,
	"user-management.iam_roles":                                  separateDrillAssertion,
	"user-management.iam_service_account_roles":                  indirectTable,
	"user-management.iam_service_account_secrets":                indirectTable,
	"user-management.iam_service_accounts":                       representedTenantPredicate,
	"user-management.iam_sso_link_requests":                      indirectTable,
	"user-management.iam_tenant_purge_stores":                    indirectTable,
	"user-management.iam_tenant_purges":                          indirectTable,
	"user-management.iam_tenant_tiers":                           indirectTable,
	"user-management.iam_tenant_vpartition_overrides":            indirectTable,
	"user-management.iam_tenants":                                indirectTable,
	"user-management.iam_vpartition_assignments":                 indirectTable,
	"user-management.purged_tenants":                             purgeTombstone,
	"user-management.secrets":                                    representedTenantPredicate,
	"user-management.signing_keys":                               indirectTable,
	"user-management.system_settings":                            indirectTable,
	"user-management.user_management_migrations":                 migrationTable,
}

func checkPurgeSeedCoverage(tables []string, seeds []seededLocation, exemptions map[string]string) error {
	catalog := map[string]bool{}
	for _, name := range tables {
		catalog[name] = true
	}
	covered := map[string]bool{}
	for _, seed := range seeds {
		if !catalog[seed.label] || covered[seed.label] || seed.rows <= 0 || strings.TrimSpace(seed.where) == "" {
			return fmt.Errorf("invalid or stale seeded assertion for %s", seed.label)
		}
		covered[seed.label] = true
	}
	for name, reason := range exemptions {
		if !catalog[name] || covered[name] || strings.TrimSpace(reason) == "" {
			return fmt.Errorf("invalid, stale or overlapping drill exemption for %s", name)
		}
		if strings.HasSuffix(name, "_versions") {
			return fmt.Errorf("published versions must be seeded: %s", name)
		}
		covered[name] = true
	}
	var missing []string
	for name := range catalog {
		if !covered[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		return fmt.Errorf("tables lack seeded purge assertions or reasoned drill exemptions: %s", strings.Join(missing, ", "))
	}
	return nil
}

func TestPurgeDrillSeedCoverage(t *testing.T) {
	files, err := filepath.Glob("golden/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(areas) {
		t.Fatalf("got %d golden schemas for %d areas", len(files), len(areas))
	}
	tableStart := regexp.MustCompile(`(?m)^CREATE TABLE "([^"]+)"\.([a-z_][a-z_0-9]*) \(`)
	var tables []string
	seenSchemas := map[string]bool{}
	for _, file := range files {
		seenSchemas[strings.TrimSuffix(filepath.Base(file), ".sql")] = true
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		matches := tableStart.FindAllStringSubmatch(string(data), -1)
		if len(matches) == 0 || len(matches) != strings.Count(string(data), "CREATE TABLE ") {
			t.Fatalf("unrecognized or empty table catalog: %s", file)
		}
		for _, m := range matches {
			tables = append(tables, m[1]+"."+m[2])
		}
	}
	for _, area := range areas {
		if !seenSchemas[area.name] {
			t.Fatalf("missing golden schema %s", area.name)
		}
	}
	if err := checkPurgeSeedCoverage(tables, tenantRowCounts(), purgeSeedExemptions); err != nil {
		t.Fatal(err)
	}
}

func TestPurgeSeedCoverageRefusals(t *testing.T) {
	seed := seededLocation{"area.devices", `"area".devices`, "tenant_id = ?", 1}
	tests := []struct {
		name       string
		tables     []string
		seeds      []seededLocation
		exemptions map[string]string
		want       string
	}{
		{"new table", []string{"area.devices", "area.new"}, []seededLocation{seed}, nil, "area.new"},
		{"removed seed", []string{"area.devices"}, nil, nil, "area.devices"},
		{"empty reason", []string{"area.other"}, nil, map[string]string{"area.other": " "}, "area.other"},
		{"stale exemption", []string{"area.devices"}, []seededLocation{seed}, map[string]string{"area.gone": "removed"}, "area.gone"},
		{"stale seed", nil, []seededLocation{seed}, nil, "area.devices"},
		{"duplicate seed", []string{"area.devices"}, []seededLocation{seed, seed}, nil, "area.devices"},
		{"overlap", []string{"area.devices"}, []seededLocation{seed}, map[string]string{"area.devices": "redundant"}, "area.devices"},
		{"published version", []string{"area.profile_versions"}, nil, map[string]string{"area.profile_versions": "must not excuse history"}, "published versions must be seeded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkPurgeSeedCoverage(tt.tables, tt.seeds, tt.exemptions)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v; want refusal naming %s", err, tt.want)
			}
		})
	}
	if err := checkPurgeSeedCoverage([]string{"area.devices", "area.other"}, []seededLocation{seed}, map[string]string{"area.other": "verified elsewhere"}); err != nil {
		t.Fatal(err)
	}
	seed.rows = 0
	if err := checkPurgeSeedCoverage([]string{"area.devices"}, []seededLocation{seed}, nil); err == nil {
		t.Fatal("zero-row seed must fail")
	}
}

// Listing a row assertion must not count as seeding the integration fixture.
func TestPurgeDrillAssertionsHaveInserts(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "purge_drill_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	inserts := map[string]bool{}
	insert := regexp.MustCompile(`^INSERT INTO "([^"]+)"\.([a-z_][a-z_0-9]*)\s`)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "seedTenant" {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) < 3 {
				return true
			}
			name, ok := call.Fun.(*ast.Ident)
			if !ok || name.Name != "mustExec" {
				return true
			}
			literal, ok := call.Args[2].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			sql, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatal(err)
			}
			if m := insert.FindStringSubmatch(sql); m != nil {
				inserts[m[1]+"."+m[2]] = true
			}
			return true
		})
	}
	for _, seed := range tenantRowCounts() {
		if !inserts[seed.label] {
			t.Errorf("%s has a row assertion but no seedTenant INSERT", seed.label)
		}
	}
}

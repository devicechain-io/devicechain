-- Copyright The DeviceChain Authors
-- SPDX-License-Identifier: Apache-2.0

-- Captured from the released v1.9.0 DeleteDevice and deleteEdgeEntity methods
-- (8979efcc8590eadbf0875691e6b981aa9461f08c) in the posting fixture. Only the
-- method names were changed to coexist with this release during capture.
-- DELETE callback output, in execution order: quotes normalized for PostgreSQL,
-- values parameterized. One SHARED k attribute, no optional children or kicker.
-- K_dev and the protocol marker precede these statements in the replay runner.
DELETE FROM "entity_relationships" WHERE ((source_type = 'device' AND source_id = @id) OR (target_type = 'device' AND target_id = @id)) AND "entity_relationships"."tenant_id" = @tenant;
DELETE FROM "entity_attributes" WHERE "entity_attributes"."tenant_id" = @tenant AND "entity_attributes"."id" = @attribute_id;
DELETE FROM "entity_attribute_generations" WHERE device_id = @id AND "entity_attribute_generations"."tenant_id" = @tenant;
DELETE FROM "alarms" WHERE (originator_type = 'device' AND originator_id = @id) AND "alarms"."tenant_id" = @tenant RETURNING *;
DELETE FROM "entity_group_memberships" WHERE (entity_type = 'device' AND entity_id = @id) AND "entity_group_memberships"."tenant_id" = @tenant;
DELETE FROM "device_credentials" WHERE device_id = @id AND "device_credentials"."tenant_id" = @tenant RETURNING "tenant_id","device_id";
DELETE FROM "device_replacements" WHERE device_id = @id AND "device_replacements"."tenant_id" = @tenant;
DELETE FROM "device_aliases" WHERE device_id = @id AND "device_aliases"."tenant_id" = @tenant;
DELETE FROM "device_configuration_reports" WHERE device_id = @id AND "device_configuration_reports"."tenant_id" = @tenant;
DELETE FROM "device_configuration_revisions" WHERE device_id = @id AND "device_configuration_revisions"."tenant_id" = @tenant;
DELETE FROM "device_configuration_states" WHERE device_id = @id AND "device_configuration_states"."tenant_id" = @tenant;
DELETE FROM "devices" WHERE token = @token AND "devices"."tenant_id" = @tenant;

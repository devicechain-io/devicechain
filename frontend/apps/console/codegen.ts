// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
import type { CodegenConfig } from '@graphql-codegen/cli';

const config: CodegenConfig = {
  ignoreNoDocuments: true,
  generates: {
    './src/gql/user-management/': {
      schema: '../../../backend/services/user-management/graphql/schema.gql',
      documents: ['src/lib/api/user-management.ts'],
      preset: 'client',
      presetConfig: { fragmentMasking: false },
      config: { documentMode: 'string' },
    },
    './src/gql/device-management/': {
      schema: '../../../backend/services/device-management/graphql/schema.graphql',
      // The device, asset, customer, and area registry families are all served by
      // the one device-management schema, so they share a single generated client.
      documents: [
        'src/lib/api/device-management.ts',
        'src/lib/api/assets.ts',
        'src/lib/api/customers.ts',
        'src/lib/api/areas.ts',
        'src/lib/api/relationships.ts',
        'src/lib/api/audit.ts',
        'src/lib/api/credentials.ts',
        'src/lib/api/alarms.ts',
      ],
      preset: 'client',
      presetConfig: { fragmentMasking: false },
      config: { documentMode: 'string' },
    },
    './src/gql/dashboard-management/': {
      schema: '../../../backend/services/dashboard-management/graphql/schema.graphql',
      documents: ['src/lib/api/dashboards.ts'],
      preset: 'client',
      presetConfig: { fragmentMasking: false },
      config: { documentMode: 'string' },
    },
    './src/gql/device-state/': {
      schema: '../../../backend/services/device-state/graphql/schema.graphql',
      documents: ['src/lib/api/device-state.ts'],
      preset: 'client',
      presetConfig: { fragmentMasking: false },
      config: { documentMode: 'string' },
    },
    './src/gql/event-management/': {
      schema: '../../../backend/services/event-management/graphql/schema.graphql',
      documents: ['src/lib/api/event-management.ts'],
      preset: 'client',
      presetConfig: { fragmentMasking: false },
      config: { documentMode: 'string' },
    },
    './src/gql/command-delivery/': {
      schema: '../../../backend/services/command-delivery/graphql/schema.graphql',
      documents: ['src/lib/api/command-delivery.ts'],
      preset: 'client',
      presetConfig: { fragmentMasking: false },
      config: { documentMode: 'string' },
    },
    // The DETECT rule compiler validation gate (ADR-051 / ADR-044), served by
    // event-processing. The console calls validateDetectionRules to show a rule's
    // compile/cost diagnostics inline while authoring, before the profile publish
    // re-validates authoritatively (slice 7a-2).
    './src/gql/event-processing/': {
      schema: '../../../backend/services/event-processing/graphql/schema.graphql',
      documents: ['src/lib/api/event-processing.ts'],
      preset: 'client',
      presetConfig: { fragmentMasking: false },
      config: { documentMode: 'string' },
    },
    // The per-tenant outbound-connectors CRUD (ADR-060 C5), served by the
    // outbound-connectors service. Its own schema + client; the console lists a
    // tenant's connectors for the `publish` action's picker and authors them here.
    './src/gql/outbound-connectors/': {
      schema: '../../../backend/services/outbound-connectors/graphql/schema.graphql',
      documents: ['src/lib/api/connectors.ts'],
      preset: 'client',
      presetConfig: { fragmentMasking: false },
      config: { documentMode: 'string' },
    },
    // The instance-scoped admin API (ADR-033), served by user-management at
    // /admin/graphql. Its own schema + client so the admin console's typed
    // operations never mix with the tenant-scoped user-management ones.
    './src/gql/user-management-admin/': {
      schema: '../../../backend/services/user-management/graphql/admin_schema.gql',
      documents: ['src/lib/api/admin.ts'],
      preset: 'client',
      presetConfig: { fragmentMasking: false },
      config: { documentMode: 'string' },
    },
    // The instance-scoped settings API (ADR-042 P2), served by user-management at
    // /settings/graphql on the same identity-token lane as the admin API. Its own
    // schema + client so its typed operations stay separate.
    './src/gql/user-management-settings/': {
      schema: '../../../backend/services/user-management/graphql/settings_schema.gql',
      documents: ['src/lib/api/settings.ts'],
      preset: 'client',
      presetConfig: { fragmentMasking: false },
      config: { documentMode: 'string' },
    },
  },
};
export default config;

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
import type { CodegenConfig } from '@graphql-codegen/cli';

const config: CodegenConfig = {
  ignoreNoDocuments: true,
  generates: {
    './src/gql/user-management/': {
      schema: '../backend/services/user-management/graphql/schema.gql',
      documents: ['src/lib/api/user-management.ts'],
      preset: 'client',
      presetConfig: { fragmentMasking: false },
      config: { documentMode: 'string' },
    },
    './src/gql/device-management/': {
      schema: '../backend/services/device-management/graphql/schema.graphql',
      documents: ['src/lib/api/device-management.ts'],
      preset: 'client',
      presetConfig: { fragmentMasking: false },
      config: { documentMode: 'string' },
    },
    './src/gql/device-state/': {
      schema: '../backend/services/device-state/graphql/schema.graphql',
      documents: ['src/lib/api/device-state.ts'],
      preset: 'client',
      presetConfig: { fragmentMasking: false },
      config: { documentMode: 'string' },
    },
    './src/gql/event-management/': {
      schema: '../backend/services/event-management/graphql/schema.graphql',
      documents: ['src/lib/api/event-management.ts'],
      preset: 'client',
      presetConfig: { fragmentMasking: false },
      config: { documentMode: 'string' },
    },
    // The instance-scoped admin API (ADR-033), served by user-management at
    // /admin/graphql. Its own schema + client so the admin console's typed
    // operations never mix with the tenant-scoped user-management ones.
    './src/gql/user-management-admin/': {
      schema: '../backend/services/user-management/graphql/admin_schema.gql',
      documents: ['src/lib/api/admin.ts'],
      preset: 'client',
      presetConfig: { fragmentMasking: false },
      config: { documentMode: 'string' },
    },
  },
};
export default config;

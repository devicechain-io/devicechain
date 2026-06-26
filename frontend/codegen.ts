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
  },
};
export default config;

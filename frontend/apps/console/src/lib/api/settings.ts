// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the instance-scoped settings API (ADR-042 P2),
// served by user-management at /settings/graphql. Every call authenticates with
// the identity token ({ identity: true }) and is authorized server-side on a
// settings authority (superusers hold "*").
//
// Values are opaque JSON exposed as JSON-encoded strings; the console interprets
// them (e.g. the token-mask setting drives token generation).
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/user-management-settings';
import type { SettingsQuery } from '@/gql/user-management-settings/graphql';

// The effective setting type derives from the generated query result so it can
// never drift from the schema.
export type Setting = SettingsQuery['settings'][number];

// ── Query ───────────────────────────────────────────────────────────────

const SETTINGS = graphql(`
  query Settings {
    settings {
      key
      description
      value
      overridden
      updatedAt
      updatedBy
    }
  }
`);

export async function listSettings(): Promise<Setting[]> {
  const data = await gql('user-management/settings', SETTINGS, undefined, { identity: true });
  return data.settings;
}

// ── Mutations ───────────────────────────────────────────────────────────

const SET_SETTING = graphql(`
  mutation SetSetting($key: String!, $value: String!) {
    setSetting(key: $key, value: $value) {
      key
      description
      value
      overridden
      updatedAt
      updatedBy
    }
  }
`);

// setSetting overrides a setting with a JSON value; the value must be valid JSON.
export async function setSetting(key: string, value: string): Promise<Setting> {
  const data = await gql('user-management/settings', SET_SETTING, { key, value }, { identity: true });
  return data.setSetting;
}

const CLEAR_SETTING = graphql(`
  mutation ClearSetting($key: String!) {
    clearSetting(key: $key) {
      key
      description
      value
      overridden
      updatedAt
      updatedBy
    }
  }
`);

// clearSetting removes a setting's override, reverting it to the code default.
export async function clearSetting(key: string): Promise<Setting> {
  const data = await gql('user-management/settings', CLEAR_SETTING, { key }, { identity: true });
  return data.clearSetting;
}

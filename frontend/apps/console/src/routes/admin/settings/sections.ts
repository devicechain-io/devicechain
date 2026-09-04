// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The console's editor for each system-setting key, by key.
//
// This map is deliberately PARTIAL, and the settings page falls back to a raw
// JSON editor for anything missing. The vocabulary of settings lives in the
// backend (the settingsdefs Go package), so the console cannot be exhaustive over
// it by construction — a newer instance can serve a key an older console has
// never heard of. Falling back keeps that key editable instead of invisible.
//
// The reverse — a section here for a key the server does not define — is caught
// by sections.test.ts, which is the only direction a test CAN check: a stale
// entry would render a tab whose every save is rejected with "unknown setting".

import type { SettingSection } from './registry';
import { tokenMasksSection } from './TokenMasksEditor';
import { basemapDefaultSection } from './BasemapDefaultEditor';
import { brandingDefaultSection } from './BrandingDefaultEditor';
import { localeDefaultSection } from './LocaleDefaultEditor';

export const SECTIONS: Record<string, SettingSection> = Object.fromEntries(
  [tokenMasksSection, basemapDefaultSection, brandingDefaultSection, localeDefaultSection].map(
    (s) => [s.key, s],
  ),
);

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What a restore or copy would change, key by key: the device's current desired document
// against the document it will hold afterwards. Both are canonical JSON objects of key to
// value, so a key's value is compared by its canonical text.

export type ConfigurationChangeKind = 'added' | 'changed' | 'removed' | 'unchanged';

export interface ConfigurationChange {
  key: string;
  kind: ConfigurationChangeKind;
  // The value's JSON text before and after; null where the key is absent.
  before: string | null;
  after: string | null;
}

function entries(document: string | null): Map<string, string> {
  const out = new Map<string, string>();
  if (!document) return out;
  const parsed: unknown = JSON.parse(document);
  if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
    for (const [key, value] of Object.entries(parsed as Record<string, unknown>)) {
      out.set(key, JSON.stringify(value));
    }
  }
  return out;
}

// diffConfigurationDocuments lists every key of either document, sorted, with how it
// changes. current is null when the device has no revision yet.
export function diffConfigurationDocuments(current: string | null, proposed: string): ConfigurationChange[] {
  const before = entries(current);
  const after = entries(proposed);
  const keys = [...new Set([...before.keys(), ...after.keys()])].sort();
  return keys.map((key) => {
    const b = before.get(key) ?? null;
    const a = after.get(key) ?? null;
    let kind: ConfigurationChangeKind;
    if (b === null) kind = 'added';
    else if (a === null) kind = 'removed';
    else kind = a === b ? 'unchanged' : 'changed';
    return { key, kind, before: b, after: a };
  });
}

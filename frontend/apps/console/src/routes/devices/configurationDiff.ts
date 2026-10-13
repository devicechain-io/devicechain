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

// entries maps each top-level key of a JSON object document to its value's text EXACTLY as
// the document spells it. It does not round-trip values through JSON.parse: a LONG beyond
// 2^53 (9007199254740993) parses to the nearest double, so two different values would be
// shown, and compared, as the same number. The server's documents are canonical, so equal
// values have equal text. The document is validated with JSON.parse first; anything that
// is not an object has no keys.
function entries(document: string | null): Map<string, string> {
  const out = new Map<string, string>();
  if (!document) return out;
  const parsed: unknown = JSON.parse(document);
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return out;

  let i = document.indexOf('{') + 1;
  const skipSpace = () => {
    while (i < document.length && /\s/.test(document[i])) i++;
  };
  // The index just past the string token that starts (with its '"') at from.
  const endOfString = (from: number) => {
    let j = from + 1;
    while (j < document.length && document[j] !== '"') j += document[j] === '\\' ? 2 : 1;
    return j + 1;
  };
  for (;;) {
    skipSpace();
    if (i >= document.length || document[i] === '}') break;
    if (document[i] === ',') {
      i++;
      continue;
    }
    const keyEnd = endOfString(i);
    const key = JSON.parse(document.slice(i, keyEnd)) as string;
    i = keyEnd;
    skipSpace();
    i++; // the ':'
    skipSpace();
    const start = i;
    let depth = 0;
    while (i < document.length) {
      const c = document[i];
      if (c === '"') {
        i = endOfString(i);
        continue;
      }
      if (c === '{' || c === '[') depth++;
      else if (c === '}' || c === ']') {
        if (depth === 0) break;
        depth--;
      } else if (c === ',' && depth === 0) break;
      i++;
    }
    out.set(key, document.slice(start, i).trim());
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

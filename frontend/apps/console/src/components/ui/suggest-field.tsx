// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A free-text input backed by suggestions from the values a discovery facet already
// holds (ADR-045 decision 8) — so a tenant's fleet vocabulary stays consistent
// ("Acme" not also "ACME"/"Acme Corp") without constraining input. A native
// <datalist> gives browser-native suggestion + filtering; the fetched list only
// hints — the user can always type a brand-new value past it. Mirrors TokenField's
// shape (a plain Input plus auxiliary data that hints, never blocks).

import { useEffect, useState } from 'react';
import { Input } from '@/components/ui/input';
import { getFacetValues, type DeviceFacet } from '@/lib/api/device-management';

export function SuggestField({
  id,
  facet,
  value,
  onChange,
  placeholder,
}: {
  id: string;
  /** Which discovery facet's in-use values to suggest. */
  facet: DeviceFacet;
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
}) {
  const [suggestions, setSuggestions] = useState<string[]>([]);
  const listId = `${id}-suggestions`;

  useEffect(() => {
    let cancelled = false;
    // A load failure just means no suggestions — the field still works as free text.
    getFacetValues(facet)
      .then((values) => {
        if (!cancelled) setSuggestions(values);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [facet]);

  return (
    <>
      <Input
        id={id}
        list={listId}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        autoComplete="off"
      />
      <datalist id={listId}>
        {suggestions.map((s) => (
          <option key={s} value={s} />
        ))}
      </datalist>
    </>
  );
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Checkbox } from '@/components/ui/checkbox';

/** The `*` super-authority: every authority at the role's tier. */
export const ALL_AUTHORITIES = '*';

/**
 * The authorities grouped by the area they act on — the part before the colon, so
 * `device:read` and `device:write` sit together under `device` — with the groups and
 * the authorities inside them sorted. `*` is not in any group; the checklist shows it
 * on its own.
 */
export function groupAuthorities(vocabulary: string[]): { area: string; authorities: string[] }[] {
  const groups = new Map<string, string[]>();
  for (const a of vocabulary) {
    if (a === ALL_AUTHORITIES) continue;
    const i = a.indexOf(':');
    const area = i > 0 ? a.slice(0, i) : a;
    groups.set(area, [...(groups.get(area) ?? []), a]);
  }
  return [...groups.entries()]
    .sort(([x], [y]) => x.localeCompare(y))
    .map(([area, authorities]) => ({ area, authorities: [...authorities].sort() }));
}

/**
 * True when someone holding `held` may grant `authority`: they hold it themselves, or
 * hold `*`. `*` itself only by a holder of `*`. This mirrors the server's rule so the
 * checklist can grey out what a save would refuse; the server still decides.
 */
export function mayGrant(held: string[], authority: string): boolean {
  return held.includes(ALL_AUTHORITIES) || held.includes(authority);
}

// AuthorityChecklist picks a role's authorities from a vocabulary, grouped by area.
// Authorities the editing person does not hold themselves are shown but greyed out:
// granting one would be refused (no one can hand out more than they have), and hiding
// them would make a role that already carries one look like it carries less.
export function AuthorityChecklist({
  vocabulary,
  value,
  onChange,
  held,
  readOnly = false,
}: {
  vocabulary: string[];
  value: string[];
  onChange: (next: string[]) => void;
  /** The authorities of the person editing; `*` means all of them. */
  held: string[];
  readOnly?: boolean;
}) {
  const { t } = useTranslation('members');
  const groups = useMemo(() => groupAuthorities(vocabulary), [vocabulary]);
  const selected = new Set(value);
  const all = selected.has(ALL_AUTHORITIES);

  const toggle = (authority: string, on: boolean) => {
    const next = new Set(selected);
    if (on) next.add(authority);
    else next.delete(authority);
    onChange([...next].sort());
  };

  const row = (authority: string, label: string, hint?: string) => {
    const grantable = mayGrant(held, authority);
    // An authority the role already carries stays removable even when the editor does not
    // hold it: taking a grant away is never an escalation.
    const disabled = readOnly || (!grantable && !selected.has(authority)) || (all && authority !== ALL_AUTHORITIES);
    const id = `auth-${authority.replace(/[^a-z0-9]/gi, '-')}`;
    return (
      <label
        key={authority}
        htmlFor={id}
        className={`flex items-start gap-2 text-sm ${disabled ? 'cursor-not-allowed opacity-60' : 'cursor-pointer'}`}
        title={!grantable ? t('authorityNotHeld') : undefined}
      >
        <Checkbox
          id={id}
          checked={selected.has(authority) || (all && authority !== ALL_AUTHORITIES)}
          disabled={disabled}
          onCheckedChange={(v) => toggle(authority, v === true)}
          className="mt-0.5"
        />
        <span className="flex flex-col">
          <span className="font-mono text-xs">{label}</span>
          {hint && <span className="text-xs text-muted-foreground">{hint}</span>}
        </span>
      </label>
    );
  };

  return (
    <div className="space-y-4" data-shot="authority-checklist">
      {vocabulary.includes(ALL_AUTHORITIES) && (
        <div className="rounded-md border border-border p-3">{row(ALL_AUTHORITIES, ALL_AUTHORITIES, t('authorityAllHint'))}</div>
      )}
      <div className="grid gap-3 sm:grid-cols-2">
        {groups.map((g) => (
          <fieldset key={g.area} className="space-y-1.5 rounded-md border border-border p-3">
            <legend className="px-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">{g.area}</legend>
            {g.authorities.map((a) => row(a, a))}
          </fieldset>
        ))}
      </div>
    </div>
  );
}

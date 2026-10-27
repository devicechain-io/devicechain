// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The device form's alias field: the names a sender other than the platform knows the device by.
// One row per scheme (a DevEUI, a network server's device id, or a name the tenant chose). The
// rules live in deviceAliases.ts; this component only lays them out.

import { useTranslation } from 'react-i18next';
import { Plus, X } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { FormField } from '@/components/ui/form-field';
import { IconButton } from '@/components/ui/icon-button';
import { Input } from '@/components/ui/input';
import {
  ALIAS_KINDS,
  MAX_ALIASES,
  aliasProblems,
  emptyAliasRow,
  kindKey,
  type AliasKind,
  type AliasRow,
} from './deviceAliases';

export function DeviceAliasesField({
  rows,
  onChange,
  disabled = false,
}: {
  rows: AliasRow[];
  onChange: (rows: AliasRow[]) => void;
  disabled?: boolean;
}) {
  const { t } = useTranslation('devices');
  const problems = aliasProblems(rows);
  const kindOptions: ComboboxOption[] = ALIAS_KINDS.map((k) => ({ value: k, label: t(`aliasKind_${kindKey(k)}`) }));
  const patch = (i: number, p: Partial<AliasRow>) => onChange(rows.map((r, j) => (j === i ? { ...r, ...p } : r)));

  return (
    <FormField label={t('aliasesLabel')} htmlFor="device-alias-0" description={t('aliasesHelp')}>
      <div className="space-y-2" data-shot="device-aliases">
        {rows.map((row, i) => {
          const problem = problems[i];
          return (
            <div key={i} className="space-y-1">
              <div className="flex items-start gap-2">
                <div className="w-44 shrink-0">
                  <Combobox
                    id={`device-alias-kind-${i}`}
                    value={row.kind}
                    onChange={(v) => patch(i, { kind: v as AliasKind })}
                    options={kindOptions}
                    disabled={disabled}
                    allowClear={false}
                  />
                </div>
                {row.kind === 'custom' && (
                  <Input
                    aria-label={t('aliasCustomNameLabel')}
                    value={row.customName}
                    disabled={disabled}
                    onChange={(e) => patch(i, { customName: e.target.value })}
                    placeholder={t('aliasCustomNamePlaceholder')}
                    className="w-36 shrink-0 font-mono text-xs"
                  />
                )}
                <Input
                  id={`device-alias-${i}`}
                  aria-label={t('aliasValueLabel')}
                  aria-invalid={problem ? true : undefined}
                  value={row.value}
                  disabled={disabled}
                  onChange={(e) => patch(i, { value: e.target.value })}
                  placeholder={t(`aliasPlaceholder_${kindKey(row.kind)}`)}
                  className="font-mono text-xs"
                  autoComplete="off"
                />
                {!disabled && (
                  <IconButton
                    label={t('aliasRemove')}
                    size="sm"
                    onClick={() => onChange(rows.filter((_, j) => j !== i))}
                  >
                    <X size={14} />
                  </IconButton>
                )}
              </div>
              {problem && (
                <p role="alert" className="text-xs text-destructive">
                  {t(problem === 'value' ? `aliasProblem_value_${kindKey(row.kind)}` : `aliasProblem_${problem}`)}
                </p>
              )}
            </div>
          );
        })}
        {!disabled && rows.length < MAX_ALIASES && (
          <Button variant="outline" size="sm" onClick={() => onChange([...rows, nextRow(rows)])}>
            <Plus size={14} /> {t('aliasAdd')}
          </Button>
        )}
      </div>
    </FormField>
  );
}

// A new row starts on the first scheme the device does not already have, so adding a second
// alias does not open on a duplicate.
function nextRow(rows: AliasRow[]): AliasRow {
  const free = ALIAS_KINDS.find((k) => k === 'custom' || !rows.some((r) => r.kind === k));
  return { ...emptyAliasRow(), kind: free ?? 'custom' };
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Test vectors: a saved input with the readings it must produce. They are saved with the decoder,
// re-run as the decoder is edited, and every one must pass before the profile can be published.

import { useId } from 'react';
import { useTranslation } from 'react-i18next';
import { Plus, Trash2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { Select, SelectItem } from '@/components/ui/select';
import { Badge } from '@/components/ui/badge';
import { IconButton } from '@/components/ui/icon-button';
import { cn } from '@/lib/utils';
import { LIMITS, VECTOR_INPUT_KINDS, blankVector, defaultVectorKind, newId, pathHits, type Issue, type InputKind, type VectorState, type VectorInputKind } from './model';
import '@/i18n/lazy/deviceProfiles';

export interface VectorOutcome {
  name: string;
  ok: boolean;
  error?: string | null;
  cost: number;
}

function Cell({ label, htmlFor, className, children }: { label: string; htmlFor: string; className?: string; children: React.ReactNode }) {
  return (
    <div className={cn('min-w-0', className)}>
      <label htmlFor={htmlFor} className="mb-1 block text-xs font-medium text-muted-foreground">
        {label}
      </label>
      {children}
    </div>
  );
}

function VectorEditor(props: {
  v: VectorState;
  index: number;
  outcome: VectorOutcome | null;
  issues: Issue[];
  named: string[];
  onChange: (v: VectorState) => void;
  onRemove: () => void;
}) {
  const { v, index, outcome, issues, named, onChange, onRemove } = props;
  const { t } = useTranslation('deviceProfiles');
  const base = useId();
  const path = `vectors[${index}]`;
  const marked = pathHits(named, path);
  const mine = issues.filter((i) => i.path === path || i.path.startsWith(path + '.') || i.path.startsWith(path + '['));
  const isFrame = v.kind === 'frameHex' || v.kind === 'frameBase64';

  return (
    <li
      data-row-path={path}
      aria-invalid={mine.length > 0 || marked || undefined}
      className={cn('space-y-3 rounded-md border p-3', marked || (outcome && !outcome.ok) ? 'border-destructive' : 'border-border')}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="grid flex-1 grid-cols-2 gap-3 sm:grid-cols-4">
          <Cell label={t('decVectorName')} htmlFor={`${base}-name`} className="col-span-2">
            <Input id={`${base}-name`} value={v.name} onChange={(e) => onChange({ ...v, name: e.target.value })} />
          </Cell>
          <Cell label={t('decVectorInput')} htmlFor={`${base}-kind`}>
            <Select id={`${base}-kind`} value={v.kind} onChange={(k) => onChange({ ...v, kind: k as VectorInputKind })}>
              {VECTOR_INPUT_KINDS.map((k) => (
                <SelectItem key={k} value={k}>
                  {t(`decVectorKind.${k}`)}
                </SelectItem>
              ))}
            </Select>
          </Cell>
          {isFrame && (
            <div className="grid grid-cols-2 gap-2">
              <Cell label={t('decFPort')} htmlFor={`${base}-port`}>
                <Input id={`${base}-port`} inputMode="numeric" value={v.fPort} onChange={(e) => onChange({ ...v, fPort: e.target.value })} />
              </Cell>
              <Cell label={t('decTestFCnt')} htmlFor={`${base}-cnt`}>
                <Input id={`${base}-cnt`} inputMode="numeric" value={v.fCnt} onChange={(e) => onChange({ ...v, fCnt: e.target.value })} />
              </Cell>
            </div>
          )}
        </div>
        <IconButton label={t('decRemoveVector', { name: v.name || index + 1 })} onClick={onRemove}>
          <Trash2 size={14} />
        </IconButton>
      </div>
      <Cell label={t('decVectorPayload')} htmlFor={`${base}-text`}>
        <Textarea id={`${base}-text`} rows={isFrame ? 2 : 5} spellCheck={false} value={v.text} onChange={(e) => onChange({ ...v, text: e.target.value })} />
      </Cell>

      <fieldset className="space-y-2">
        <legend className="mb-1 text-xs font-medium text-muted-foreground">{t('decVectorExpect')}</legend>
        {v.expect.map((e, ei) => (
          <div key={e.id} className="flex items-end gap-2">
            <Cell label={t('decTestKey')} htmlFor={`${base}-ek-${e.id}`} className="flex-1">
              <Input id={`${base}-ek-${e.id}`} className="font-mono" value={e.key} onChange={(ev) => onChange({ ...v, expect: v.expect.map((x) => (x.id === e.id ? { ...x, key: ev.target.value } : x)) })} />
            </Cell>
            <Cell label={t('decTestValue')} htmlFor={`${base}-ev-${e.id}`} className="w-32">
              <Input id={`${base}-ev-${e.id}`} inputMode="decimal" value={e.value} onChange={(ev) => onChange({ ...v, expect: v.expect.map((x) => (x.id === e.id ? { ...x, value: ev.target.value } : x)) })} />
            </Cell>
            <IconButton label={t('decRemoveExpect', { n: ei + 1 })} onClick={() => onChange({ ...v, expect: v.expect.filter((x) => x.id !== e.id) })}>
              <Trash2 size={14} />
            </IconButton>
          </div>
        ))}
        <Button size="sm" variant="outline" onClick={() => onChange({ ...v, expect: [...v.expect, { id: newId(), key: '', value: '' }] })}>
          <Plus size={14} /> {t('decVectorAddExpect')}
        </Button>
      </fieldset>

      {mine.length > 0 && (
        <ul className="space-y-0.5 text-xs text-destructive">
          {mine.map((i, n) => (
            <li key={n}>{t(`decIssue.${i.code}`, i.params)}</li>
          ))}
        </ul>
      )}
      {outcome && (
        <div className="flex flex-wrap items-center gap-2 text-sm" data-testid="vector-outcome">
          <Badge variant={outcome.ok ? 'success' : 'destructive'}>{outcome.ok ? t('decVectorPass') : t('decVectorFail')}</Badge>
          {outcome.error && <span className="text-destructive">{outcome.error}</span>}
          <span className="text-xs text-muted-foreground tabular-nums">{t('decTestCost', { cost: outcome.cost })}</span>
        </div>
      )}
    </li>
  );
}

export function VectorsPanel(props: {
  vectors: VectorState[];
  onChange: (v: VectorState[]) => void;
  outcomes: VectorOutcome[] | null;
  /** Why the decoder did not compile, when the last run said so: no vector can run then. */
  compileError: string | null;
  issues: Issue[];
  named: string[];
  inputKind: InputKind;
}) {
  const { vectors, onChange, outcomes, compileError, issues, named, inputKind } = props;
  const { t } = useTranslation('deviceProfiles');
  // Outcomes arrive in vector order; they describe THIS list only if the names still line up.
  const aligned = outcomes !== null && outcomes.length === vectors.length && outcomes.every((o, i) => o.name === vectors[i].name);
  const passed = aligned ? outcomes.filter((o) => o.ok).length : 0;

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="max-w-prose text-sm text-muted-foreground">{t('decVectorsDescription')}</p>
        {aligned && vectors.length > 0 && (
          <Badge variant={passed === vectors.length ? 'success' : 'destructive'} data-testid="vector-summary">
            {t('decVectorSummary', { passed, total: vectors.length })}
          </Badge>
        )}
      </div>
      {compileError && (
        <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {t('decVectorsNotCompiled')} {compileError}
        </p>
      )}
      {vectors.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t('decNoVectors')}</p>
      ) : (
        <ul className="space-y-3">
          {vectors.map((v, i) => (
            <VectorEditor
              key={v.id}
              v={v}
              index={i}
              outcome={aligned ? outcomes[i] : null}
              issues={issues}
              named={named}
              onChange={(next) => onChange(vectors.map((x) => (x.id === v.id ? next : x)))}
              onRemove={() => onChange(vectors.filter((x) => x.id !== v.id))}
            />
          ))}
        </ul>
      )}
      <Button
        size="sm"
        variant="outline"
        disabled={vectors.length >= LIMITS.vectors}
        onClick={() => onChange([...vectors, { ...blankVector(defaultVectorKind(inputKind)), name: t('decNewVectorName', { n: vectors.length + 1 }) }])}
      >
        <Plus size={14} /> {t('decAddVector')}
      </Button>
    </div>
  );
}

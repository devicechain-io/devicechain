// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The sections and rows of a decoder: the field table (offset, type, endianness, scale, add and a
// metric picker restricted to the profile's declared metrics) that compiles to CEL one way, raw
// CEL rows, and map rows. A row converted to CEL leaves the table for good.
//
// Every control is labelled; the labels are visible (they are short) rather than placeholders,
// because a row repeats and a placeholder disappears the moment it is typed over.

import { useId, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { ArrowDown, ArrowUp, Plus, Trash2, Code2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { Checkbox } from '@/components/ui/checkbox';
import { Select, SelectItem } from '@/components/ui/select';
import { Combobox } from '@/components/ui/combobox';
import { IconButton } from '@/components/ui/icon-button';
import { Badge } from '@/components/ui/badge';
import { cn } from '@/lib/utils';
import { EXAMPLE } from './examples';
import {
  FRAME_TYPES,
  blankSection,
  compileField,
  hasEndianness,
  newRow,
  pathHits,
  toRaw,
  LIMITS,
  type EditorState,
  type Field,
  type Issue,
  type Row,
  type SectionState,
} from './model';

export interface FieldTableProps {
  state: EditorState;
  onChange: (next: EditorState) => void;
  /** Declared, measured (not calculated) metric keys of the profile: the only scalar outputs the platform accepts. */
  metrics: string[];
  issues: Issue[];
  /** Document paths a refusal or a failed test named; the rows they locate are marked. */
  named: string[];
}

/** A labelled control cell. The label is a real <label> bound to the control by `id`. */
function Cell({ label, htmlFor, className, children }: { label: string; htmlFor: string; className?: string; children: ReactNode }) {
  return (
    <div className={cn('min-w-0', className)}>
      <label htmlFor={htmlFor} className="mb-1 block text-xs font-medium text-muted-foreground">
        {label}
      </label>
      {children}
    </div>
  );
}

function IssueList({ issues, path, id }: { issues: Issue[]; path: string; id: string }) {
  const { t } = useTranslation('deviceProfiles');
  const mine = issues.filter((i) => i.path === path || i.path.startsWith(path + '.') || i.path.startsWith(path + '['));
  if (mine.length === 0) return null;
  return (
    <ul id={id} className="mt-2 space-y-0.5 text-xs text-destructive">
      {mine.map((i, n) => (
        <li key={n}>{t(`decIssue.${i.code}`, i.params)}</li>
      ))}
    </ul>
  );
}

function KeyPicker({ id, value, metrics, onChange, label }: { id: string; value: string; metrics: string[]; onChange: (v: string) => void; label: string }) {
  const { t } = useTranslation('deviceProfiles');
  // A key the stored document names that is no longer a declared metric stays visible, so it can be seen and fixed.
  const options = (value !== '' && !metrics.includes(value) ? [value, ...metrics] : metrics).map((m) => ({ value: m }));
  return (
    <Combobox
      id={id}
      ariaLabel={label}
      options={options}
      value={value}
      onChange={onChange}
      allowClear={false}
      placeholder={t('decKeyPlaceholder')}
      searchPlaceholder={t('decKeySearch')}
      emptyMessage={t('decKeyNone')}
    />
  );
}

function ShapeCells({ f, onChange, base }: { f: Field; onChange: (f: Field) => void; base: string }) {
  const { t } = useTranslation('deviceProfiles');
  return (
    <>
      <Cell label={t('decMultiply')} htmlFor={`${base}-mul`}>
        <Input id={`${base}-mul`} inputMode="decimal" value={f.mul} onChange={(e) => onChange({ ...f, mul: e.target.value })} placeholder="1" />
      </Cell>
      <Cell label={t('decDivide')} htmlFor={`${base}-div`}>
        <Input id={`${base}-div`} inputMode="decimal" value={f.div} onChange={(e) => onChange({ ...f, div: e.target.value })} placeholder="10" />
      </Cell>
      <Cell label={t('decAdd')} htmlFor={`${base}-add`}>
        <Input id={`${base}-add`} inputMode="decimal" value={f.add} onChange={(e) => onChange({ ...f, add: e.target.value })} placeholder="-40" />
      </Cell>
    </>
  );
}

function FieldRowEditor({ f, onChange, metrics, base }: { f: Field; onChange: (f: Field) => void; metrics: string[]; base: string }) {
  const { t } = useTranslation('deviceProfiles');
  const isBits = f.type === 'bits' || f.type === 'sbits';
  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-6">
      <Cell label={t('decMetric')} htmlFor={`${base}-key`} className="col-span-2">
        <KeyPicker id={`${base}-key`} label={t('decMetric')} value={f.key} metrics={metrics} onChange={(key) => onChange({ ...f, key })} />
      </Cell>
      {f.source === 'frame' ? (
        <>
          <Cell label={t('decType')} htmlFor={`${base}-type`} className="col-span-2">
            <Select id={`${base}-type`} value={f.type} onChange={(v) => onChange({ ...f, type: v as Field['type'] })}>
              {FRAME_TYPES.map((ty) => (
                <SelectItem key={ty} value={ty}>
                  {t(`decFrameType.${ty}`)}
                </SelectItem>
              ))}
            </Select>
          </Cell>
          {hasEndianness(f.type) && (
            <Cell label={t('decEndian')} htmlFor={`${base}-endian`}>
              <Select id={`${base}-endian`} value={f.endian} onChange={(v) => onChange({ ...f, endian: v as Field['endian'] })}>
                <SelectItem value="be">{t('decEndianBig')}</SelectItem>
                <SelectItem value="le">{t('decEndianLittle')}</SelectItem>
              </Select>
            </Cell>
          )}
          <Cell label={t('decOffset')} htmlFor={`${base}-offset`}>
            <Input id={`${base}-offset`} inputMode="numeric" value={f.offset} onChange={(e) => onChange({ ...f, offset: e.target.value })} />
          </Cell>
          {(f.type === 'bit' || isBits) && (
            <Cell label={f.type === 'bit' ? t('decBitIndex') : t('decBitOffset')} htmlFor={`${base}-bit`}>
              <Input id={`${base}-bit`} inputMode="numeric" value={f.bitIndex} onChange={(e) => onChange({ ...f, bitIndex: e.target.value })} />
            </Cell>
          )}
          {isBits && (
            <Cell label={t('decBitWidth')} htmlFor={`${base}-width`}>
              <Input id={`${base}-width`} inputMode="numeric" value={f.bitWidth} onChange={(e) => onChange({ ...f, bitWidth: e.target.value })} />
            </Cell>
          )}
          {f.type === 'bcd' && (
            <Cell label={t('decBcdLength')} htmlFor={`${base}-len`}>
              <Input id={`${base}-len`} inputMode="numeric" value={f.bcdLength} onChange={(e) => onChange({ ...f, bcdLength: e.target.value })} />
            </Cell>
          )}
        </>
      ) : (
        <>
          <Cell label={t('decJsonPath')} htmlFor={`${base}-path`} className="col-span-2">
            <Input id={`${base}-path`} value={f.path} onChange={(e) => onChange({ ...f, path: e.target.value })} placeholder={EXAMPLE.jsonPath} />
          </Cell>
          <Cell label={t('decJsonType')} htmlFor={`${base}-jtype`}>
            <Select id={`${base}-jtype`} value={f.jsonType} onChange={(v) => onChange({ ...f, jsonType: v as Field['jsonType'] })}>
              <SelectItem value="number">{t('decJsonNumber')}</SelectItem>
              <SelectItem value="boolean">{t('decJsonBoolean')}</SelectItem>
            </Select>
          </Cell>
          <div className="flex items-end gap-2 pb-2">
            <Checkbox
              id={`${base}-absent`}
              checked={f.skipIfAbsent}
              onCheckedChange={(c) => onChange({ ...f, skipIfAbsent: c === true })}
            />
            <label htmlFor={`${base}-absent`} className="text-xs text-muted-foreground">
              {t('decSkipIfAbsent')}
            </label>
          </div>
        </>
      )}
      <ShapeCells f={f} onChange={onChange} base={base} />
      <Cell label={t('decRowWhen')} htmlFor={`${base}-when`} className="col-span-2">
        <Input id={`${base}-when`} className="font-mono" value={f.when} onChange={(e) => onChange({ ...f, when: e.target.value })} placeholder={EXAMPLE.rowWhen} />
      </Cell>
    </div>
  );
}

function RowEditor(props: {
  row: Row;
  input: EditorState['input'];
  onChange: (r: Row) => void;
  onRemove: () => void;
  metrics: string[];
  issues: Issue[];
  named: string[];
  path: string;
  index: number;
}) {
  const { row, input, onChange, onRemove, metrics, issues, named, path, index } = props;
  const { t } = useTranslation('deviceProfiles');
  const base = useId();
  const marked = pathHits(named, path);
  const errId = `${base}-err`;
  const hasIssues = issues.some((i) => i.path === path || i.path.startsWith(path + '.'));
  const preview = row.kind === 'field' ? compileField(row.field, input, path).out : null;

  return (
    <li
      data-row-path={path}
      aria-invalid={hasIssues || marked || undefined}
      aria-describedby={hasIssues ? errId : undefined}
      className={cn('rounded-md border p-3', marked ? 'border-destructive ring-1 ring-destructive' : 'border-border')}
    >
      <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <span className="text-xs font-medium text-muted-foreground">{t('decRowN', { n: index + 1 })}</span>
          <Badge variant={row.kind === 'field' ? 'secondary' : 'outline'}>{t(`decRowKind.${row.kind}`)}</Badge>
          {marked && <Badge variant="destructive">{t('decNamedInError')}</Badge>}
        </div>
        <div className="flex flex-wrap items-center gap-1">
          {row.kind === 'field' && (
            <Button variant="ghost" size="sm" onClick={() => onChange(toRaw(row, input))} title={t('decToCelHint')}>
              <Code2 size={14} /> {t('decToCel')}
            </Button>
          )}
          <IconButton label={t('decRemoveRow', { n: index + 1 })} onClick={onRemove}>
            <Trash2 size={14} />
          </IconButton>
        </div>
      </div>

      {row.kind === 'field' && (
        <FieldRowEditor f={row.field} onChange={(field) => onChange({ ...row, field })} metrics={metrics} base={base} />
      )}
      {row.kind === 'cel' && (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-6">
          <Cell label={t('decMetric')} htmlFor={`${base}-key`} className="sm:col-span-2">
            <KeyPicker id={`${base}-key`} label={t('decMetric')} value={row.key} metrics={metrics} onChange={(key) => onChange({ ...row, key })} />
          </Cell>
          <Cell label={t('decExpression')} htmlFor={`${base}-expr`} className="sm:col-span-4">
            <Textarea id={`${base}-expr`} rows={2} value={row.expr} onChange={(e) => onChange({ ...row, expr: e.target.value })} placeholder={EXAMPLE.celExpr} />
          </Cell>
          <Cell label={t('decRowWhen')} htmlFor={`${base}-when`} className="sm:col-span-6">
            <Input id={`${base}-when`} className="font-mono" value={row.when} onChange={(e) => onChange({ ...row, when: e.target.value })} />
          </Cell>
        </div>
      )}
      {row.kind === 'map' && (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-6">
          <Cell label={t('decMapExpression')} htmlFor={`${base}-map`} className="sm:col-span-6">
            <Textarea id={`${base}-map`} rows={2} value={row.map} onChange={(e) => onChange({ ...row, map: e.target.value })} placeholder={EXAMPLE.mapExpr} />
          </Cell>
          <Cell label={t('decMapPrefix')} htmlFor={`${base}-prefix`} className="sm:col-span-2">
            <Input id={`${base}-prefix`} className="font-mono" value={row.prefix} onChange={(e) => onChange({ ...row, prefix: e.target.value })} placeholder={EXAMPLE.prefix} />
          </Cell>
          <Cell label={t('decMaxKeys')} htmlFor={`${base}-maxkeys`}>
            <Input id={`${base}-maxkeys`} inputMode="numeric" value={row.maxKeys} onChange={(e) => onChange({ ...row, maxKeys: e.target.value })} />
          </Cell>
          <Cell label={t('decRowWhen')} htmlFor={`${base}-when`} className="sm:col-span-3">
            <Input id={`${base}-when`} className="font-mono" value={row.when} onChange={(e) => onChange({ ...row, when: e.target.value })} />
          </Cell>
        </div>
      )}

      {preview?.expr && (
        <p className="mt-2 break-all font-mono text-xs text-muted-foreground" data-testid="cel-preview">
          <span className="sr-only">{t('decGeneratedCel')}: </span>
          {preview.expr}
          {preview.when ? `  ·  ${t('decRowWhen')}: ${preview.when}` : ''}
        </p>
      )}
      <IssueList issues={issues} path={path} id={errId} />
    </li>
  );
}

function SectionEditor(props: {
  section: SectionState;
  index: number;
  count: number;
  state: EditorState;
  patch: (s: SectionState) => void;
  move: (dir: -1 | 1) => void;
  remove: () => void;
  metrics: string[];
  issues: Issue[];
  named: string[];
}) {
  const { section: s, index: si, state, patch, move, remove, metrics, issues, named } = props;
  const { t } = useTranslation('deviceProfiles');
  const base = useId();
  const path = `sections[${si}]`;
  const marked = named.some((p) => p === path || p.startsWith(path + '.when') || p.startsWith(path + '.name'));
  const sectionIssues = issues.filter((i) => i.path === `${path}.name` || i.path === `${path}.when`);
  const source = state.input === 'FRAME' ? 'frame' : 'json';
  const addRow = (kind: Row['kind']) => patch({ ...s, rows: [...s.rows, newRow(kind, source)] });
  // Hoisted: the literal-string rule reads call arguments inside JSX attributes of a component.
  const addField = () => addRow('field');
  const addCel = () => addRow('cel');
  const addMap = () => addRow('map');

  return (
    <section
      aria-label={t('decSectionN', { n: si + 1 })}
      data-row-path={path}
      className={cn('space-y-3 rounded-lg border bg-card p-4', marked ? 'border-destructive ring-1 ring-destructive' : 'border-border')}
    >
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-6">
        <Cell label={t('decSectionName')} htmlFor={`${base}-name`} className="col-span-2">
          <Input id={`${base}-name`} value={s.name} onChange={(e) => patch({ ...s, name: e.target.value })} />
        </Cell>
        {state.input === 'FRAME' && (
          <Cell label={t('decFPort')} htmlFor={`${base}-port`}>
            <Input id={`${base}-port`} inputMode="numeric" value={s.fPort} onChange={(e) => patch({ ...s, fPort: e.target.value })} placeholder="2" />
          </Cell>
        )}
        <Cell label={t('decSectionWhen')} htmlFor={`${base}-when`} className="col-span-2">
          <Input id={`${base}-when`} className="font-mono" value={s.when} onChange={(e) => patch({ ...s, when: e.target.value })} placeholder={EXAMPLE.sectionWhen} />
        </Cell>
        <div className="flex items-end gap-1 pb-1 justify-end">
          <IconButton label={t('decMoveSectionUp', { n: si + 1 })} onClick={() => move(-1)} disabled={si === 0}>
            <ArrowUp size={14} />
          </IconButton>
          <IconButton label={t('decMoveSectionDown', { n: si + 1 })} onClick={() => move(1)} disabled={si === props.count - 1}>
            <ArrowDown size={14} />
          </IconButton>
          <IconButton label={t('decRemoveSection', { n: si + 1 })} onClick={remove} disabled={props.count <= 1}>
            <Trash2 size={14} />
          </IconButton>
        </div>
      </div>
      <div className="flex items-center gap-2">
        <Checkbox id={`${base}-ignore`} checked={s.ignore} onCheckedChange={(c) => patch({ ...s, ignore: c === true })} />
        <label htmlFor={`${base}-ignore`} className="text-sm">
          {t('decIgnore')}
        </label>
        <span className="text-xs text-muted-foreground">{t('decIgnoreHint')}</span>
      </div>
      {sectionIssues.length > 0 && (
        <ul className="space-y-0.5 text-xs text-destructive">
          {sectionIssues.map((i, n) => (
            <li key={n}>{t(`decIssue.${i.code}`, i.params)}</li>
          ))}
        </ul>
      )}

      {!s.ignore && (
        <>
          {s.rows.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t('decNoRows')}</p>
          ) : (
            <ul className="space-y-3">
              {s.rows.map((r, ri) => (
                <RowEditor
                  key={r.id}
                  row={r}
                  input={state.input}
                  index={ri}
                  path={`${path}.outputs[${ri}]`}
                  metrics={metrics}
                  issues={issues}
                  named={named}
                  onChange={(next) => patch({ ...s, rows: s.rows.map((x) => (x.id === r.id ? next : x)) })}
                  onRemove={() => patch({ ...s, rows: s.rows.filter((x) => x.id !== r.id) })}
                />
              ))}
            </ul>
          )}
          <div className="flex flex-wrap gap-2">
            <Button size="sm" variant="outline" onClick={addField}>
              <Plus size={14} /> {t('decAddField')}
            </Button>
            <Button size="sm" variant="outline" onClick={addCel}>
              <Plus size={14} /> {t('decAddCel')}
            </Button>
            <Button size="sm" variant="outline" onClick={addMap}>
              <Plus size={14} /> {t('decAddMap')}
            </Button>
          </div>
        </>
      )}
    </section>
  );
}

export function FieldTable({ state, onChange, metrics, issues, named }: FieldTableProps) {
  const { t } = useTranslation('deviceProfiles');
  const setSections = (sections: SectionState[]) => onChange({ ...state, sections });
  return (
    <div className="space-y-4">
      {state.sections.map((s, si) => (
        <SectionEditor
          key={s.id}
          section={s}
          index={si}
          count={state.sections.length}
          state={state}
          metrics={metrics}
          issues={issues}
          named={named}
          patch={(next) => setSections(state.sections.map((x) => (x.id === s.id ? next : x)))}
          remove={() => setSections(state.sections.filter((x) => x.id !== s.id))}
          move={(dir) => {
            const to = si + dir;
            if (to < 0 || to >= state.sections.length) return;
            const next = [...state.sections];
            [next[si], next[to]] = [next[to], next[si]];
            setSections(next);
          }}
        />
      ))}
      <Button
        variant="outline"
        size="sm"
        disabled={state.sections.length >= LIMITS.sections}
        onClick={() => setSections([...state.sections, blankSection(t('decNewSectionName', { n: state.sections.length + 1 }))])}
      >
        <Plus size={14} /> {t('decAddSection')}
      </Button>
    </div>
  );
}

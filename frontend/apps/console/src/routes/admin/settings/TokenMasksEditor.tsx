// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The editor for entity.token_masks (ADR-042 P3): one row per entity type, each
// naming the mask its create forms mint tokens from, with a live sample of what
// that mask actually produces.
//
// It replaces a JSON textarea, and the two things it changes are the two things
// the textarea could not do:
//
//   - the entity type is PICKED, not typed. The key is the whole contract between
//     an operator's mask and the form that reads it, and a typo produced no error
//     — the form silently fell through to the "default" mask. (The console's own
//     call sites had already drifted this way: one passed "tenant tier" with a
//     space, so a mask written as "tenant-tier" never applied.)
//   - a mask that mints nothing is refused, with the reason. "dev-{sulg}" mints
//     "dev-" for every device, and nothing about the JSON said so.
//
// Both rules mirror the server, which is the authority (the tokenmask Go package).

import { useTranslation } from 'react-i18next';
import { Hash, Plus, Trash2 } from 'lucide-react';
import { sampleToken, validateMask, type MaskProblem } from '@devicechain/client';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { IconButton } from '@/components/ui/icon-button';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { EmptyState } from '@/components/ui/empty-state';
import { ENTITY_TYPES, DEFAULT_MASK_KEY } from '@/lib/entity-types';
import { defineSetting, parseJson, type SettingIssue, type SettingEditorProps } from './registry';

// The mask key that applies to every entity type without one of its own, plus the
// closed console vocabulary. `default` leads because it is the one an operator
// sets first and the one that applies everywhere.
const MASK_KEYS: string[] = [DEFAULT_MASK_KEY, ...ENTITY_TYPES];

/** One row of the editor. Form state, so both fields stay raw strings. */
interface MaskRow {
  key: string;
  mask: string;
}

interface MasksForm {
  rows: MaskRow[];
}

// The form is an ARRAY and the wire shape is an OBJECT, so two rows on the same
// key have nowhere to go. Duplicates are made unreachable rather than papered
// over: the key is a picker, and a key another row already holds is not offered.
// A key already STORED is still shown (see unknownKey below) so an unrecognised
// one stays visible and editable.
//
// Unlike the other editors this one models EVERY key by construction — any string
// is a legal entity type — so there is no known-key guard here. What it cannot
// model is a non-string VALUE, which drops to the raw editor.
function seed(json: string): MasksForm | null {
  const value = parseJson(json);
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return null;
  const entries = Object.entries(value as Record<string, unknown>);
  if (entries.some(([, mask]) => typeof mask !== 'string')) return null;
  return { rows: entries.map(([key, mask]) => ({ key, mask: mask as string })) };
}

function toJson(form: MasksForm): string {
  return JSON.stringify(Object.fromEntries(form.rows.map((r) => [r.key, r.mask])));
}

/** Maps a MaskProblem to the localized reason shown against its row. */
function maskIssue(row: MaskRow, problem: MaskProblem): SettingIssue {
  switch (problem.reason) {
    case 'empty':
      return { key: 'masksIssueEmpty', values: { key: row.key } };
    case 'unknownPlaceholder':
      return {
        key: 'masksIssueUnknownPlaceholder',
        values: { key: row.key, placeholder: problem.placeholder },
      };
    case 'noPlaceholder':
      return { key: 'masksIssueNoPlaceholder', values: { key: row.key } };
    case 'widthTooLarge':
      return {
        key: 'masksIssueWidthTooLarge',
        values: { key: row.key, placeholder: problem.placeholder, max: problem.max },
      };
    case 'invalidToken':
      return { key: 'masksIssueInvalidToken', values: { key: row.key, sample: problem.sample } };
  }
}

// Validates the produced JSON rather than the form, so what is checked is what
// would be sent — and, here, so a pair of rows that collapsed into one object key
// is judged as the single entry the server would receive.
function validate(json: string): SettingIssue | null {
  const form = seed(json);
  if (form === null) return { key: 'valueMustBeJsonError' };
  for (const row of form.rows) {
    if (row.key.trim() === '') return { key: 'masksIssueKeyRequired' };
    const problem = validateMask(row.mask);
    if (problem) return maskIssue(row, problem);
  }
  return null;
}

function TokenMasksEditor({ value, onChange }: SettingEditorProps<MasksForm>) {
  const { t } = useTranslation('adminSettings');

  const setRow = (index: number, next: Partial<MaskRow>) =>
    onChange({ rows: value.rows.map((r, i) => (i === index ? { ...r, ...next } : r)) });
  const removeRow = (index: number) =>
    onChange({ rows: value.rows.filter((_, i) => i !== index) });
  // A new row starts on the first unused key so it is immediately valid to edit;
  // when every known key is taken there is nothing left to add, and the button is
  // disabled rather than adding an empty row that cannot be filled.
  const unusedKeys = MASK_KEYS.filter((k) => !value.rows.some((r) => r.key === k));
  const addRow = () =>
    onChange({ rows: [...value.rows, { key: unusedKeys[0], mask: '{slug}' }] });

  return (
    <div className="space-y-4">
      <p className="text-xs text-muted-foreground">{t('masksPlaceholderHelp')}</p>

      {value.rows.length === 0 ? (
        <EmptyState description={t('masksEmpty')} />
      ) : (
        <ul className="space-y-2">
          {value.rows.map((row, i) => (
            <MaskRowFields
              key={i}
              row={row}
              // The row's own key stays offered even when unrecognised, so a mask
              // stored for a type this console build does not know is editable
              // rather than silently unrepresentable.
              options={[...unusedKeys, row.key].sort().map((k) => ({ value: k }))}
              unknownKey={!MASK_KEYS.includes(row.key)}
              onKeyChange={(k) => setRow(i, { key: k })}
              onMaskChange={(m) => setRow(i, { mask: m })}
              onRemove={() => removeRow(i)}
            />
          ))}
        </ul>
      )}

      <Button variant="outline" size="sm" onClick={addRow} disabled={unusedKeys.length === 0}>
        <Plus size={14} /> {t('masksAddRow')}
      </Button>
    </div>
  );
}

function MaskRowFields({
  row,
  options,
  unknownKey,
  onKeyChange,
  onMaskChange,
  onRemove,
}: {
  row: MaskRow;
  options: ComboboxOption[];
  unknownKey: boolean;
  onKeyChange: (key: string) => void;
  onMaskChange: (mask: string) => void;
  onRemove: () => void;
}) {
  const { t } = useTranslation('adminSettings');
  const problem = validateMask(row.mask);

  return (
    <li className="space-y-1">
      <div className="flex items-start gap-2">
        <div className="w-56 shrink-0">
          <Combobox
            options={options}
            value={row.key}
            onChange={onKeyChange}
            allowClear={false}
            ariaLabel={t('masksTypeColumn')}
          />
        </div>
        <Input
          value={row.mask}
          onChange={(e) => onMaskChange(e.target.value)}
          aria-label={t('masksMaskColumn')}
          className="font-mono"
        />
        {/* The sample is the point of the row: it is what an operator would
            otherwise have to save-and-go-look-for. A mask with a problem shows no
            sample — showing "dev-" beside an error would read as a result. */}
        <div className="flex h-10 w-48 shrink-0 items-center overflow-hidden">
          {problem ? (
            <span className="truncate text-xs text-destructive">{t('masksRowInvalid')}</span>
          ) : (
            <span className="truncate font-mono text-xs text-muted-foreground">
              {sampleToken(row.mask)}
            </span>
          )}
        </div>
        <IconButton
          label={t('masksRemoveRow', { key: row.key })}
          variant="quiet"
          size="md"
          onClick={onRemove}
        >
          <Trash2 size={14} />
        </IconButton>
      </div>
      {unknownKey && (
        <Badge variant="outline" className="text-muted-foreground">
          {t('masksUnknownType')}
        </Badge>
      )}
    </li>
  );
}

export const tokenMasksSection = defineSetting<MasksForm>({
  key: 'entity.token_masks',
  labelKey: 'tabTokenMasks',
  icon: Hash,
  seed,
  toJson,
  validate,
  Editor: TokenMasksEditor,
});

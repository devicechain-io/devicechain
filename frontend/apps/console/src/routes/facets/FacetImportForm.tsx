// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useMemo, useRef, useState } from 'react';
import { Download, FileUp } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { FormField } from '@/components/ui/form-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { FilePicker } from '@/components/ui/file-picker';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import { errMessage } from '@/routes/common';
import { importFacetValues } from '@/lib/api/facet-import';
import { FACET_MEMBER_TYPES } from '@/lib/api/facet-keys';
import { downloadCsv, exportFilename } from '@/lib/csv';
import {
  CsvImportError,
  IMPORT_MAX_BYTES,
  IMPORT_MAX_ROWS,
  parsePastedCsv,
  readCsvFile,
} from '@/lib/csvImport';
import {
  FACET_IMPORT_FIELDS,
  FAILED_OUTCOMES,
  ImportRunError,
  autoMap,
  buildRows,
  countOutcomes,
  failedRowsCsv,
  findDuplicates,
  mergeOutcomes,
  resultsCsv,
  RETRYABLE_OUTCOMES,
  secretFields,
  runFacetImport,
  stripSecretColumns,
  type ColumnMapping,
  type FacetImportField,
  type FileRowOutcome,
  type RowOutcome,
} from '@/routes/facets/facetImport';

const RESULTS_STEM = 'facet-values-import-results';
const FAILED_STEM = 'facet-values-import-failed';
// The most outcome rows drawn at once. A file can hold 10 000; the full list is the download.
const RENDER_CAP = 200;
const PREVIEW_ROWS = 3;

const PHASE_PICK: Phase = 'pick';
type Phase = 'pick' | 'map' | 'checked' | 'running' | 'done';
type Filter = 'ALL' | RowOutcome;

const OUTCOMES: RowOutcome[] = ['SET', 'EXISTS', 'INVALID', 'REFUSED', 'UNKNOWN'];

const FAMILY_LABEL_KEY: Record<string, string> = {
  device: 'common:familyDevice',
  asset: 'common:familyAsset',
  area: 'common:familyArea',
  customer: 'common:familyCustomer',
};

const outcomeVariant = (o: RowOutcome) =>
  o === 'SET' || o === 'EXISTS' ? 'success' : o === 'UNKNOWN' ? 'warning' : 'destructive';

// FacetImportForm sets facet values on many existing entities from a CSV file in four steps:
// read the file and map its columns (entity type, token, facet key, value), preview with a dry
// run (the server decides every row exactly as the real import will), then import in
// sequential chunks of at most 1,000 values. A value that already holds reports "unchanged",
// so a finished import can be re-run safely, and the rows the server refused can be retried
// on their own. A repeated (entity type, token, key) is caught across the WHOLE file here,
// since the server only sees one chunk at a time.
export function FacetImportForm({ onDone }: { onDone: (message: string) => void }) {
  const { t } = useTranslation('facets');
  const [paste, setPaste] = useState('');
  const [fileName, setFileName] = useState<string | null>(null);
  const [table, setTable] = useState<string[][] | null>(null);
  const [mapping, setMapping] = useState<ColumnMapping>({});
  const [fileType, setFileType] = useState('');
  const [fileKey, setFileKey] = useState('');
  const [phase, setPhase] = useState<Phase>('pick');
  const [outcomes, setOutcomes] = useState<FileRowOutcome[]>([]);
  const [progress, setProgress] = useState(0);
  const [filter, setFilter] = useState<Filter>('ALL');
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const ctl = useRef<AbortController | null>(null);

  // Closing the drawer abandons a run between chunks; whatever was answered stands on the server.
  useEffect(() => () => ctl.current?.abort(), []);

  const header = useMemo(() => table?.[0] ?? [], [table]);
  const dataRows = useMemo(() => (table ? table.slice(1) : []), [table]);
  const defaults = useMemo(() => ({ memberType: fileType, key: fileKey }), [fileType, fileKey]);
  const columnOptions: ComboboxOption[] = header.map((h, i) => ({
    value: String(i),
    label: `${i + 1}. ${h.trim() || t('importBlankColumn')}`,
  }));
  const familyOptions: ComboboxOption[] = FACET_MEMBER_TYPES.map((mt) => ({
    value: mt,
    label: t(FAMILY_LABEL_KEY[mt]),
  }));
  const familyLabel = (mt: string) => (FAMILY_LABEL_KEY[mt] ? t(FAMILY_LABEL_KEY[mt]) : mt);

  const describe = (err: unknown): string => {
    if (err instanceof CsvImportError) {
      return t(`importErr_${err.code}`, { maxMb: IMPORT_MAX_BYTES / (1024 * 1024), maxRows: IMPORT_MAX_ROWS });
    }
    return errMessage(err);
  };

  const reset = () => {
    setOutcomes([]);
    setPhase('map');
  };

  const load = (rows: string[][], name: string | null) => {
    setError(null);
    setNotice(null);
    setTable(rows);
    setFileName(name);
    setMapping(autoMap(rows[0]));
    setOutcomes([]);
    setPhase('map');
  };

  const onFile = async (file: File) => {
    try {
      load(await readCsvFile(file), file.name);
    } catch (err) {
      setError(describe(err));
    }
  };

  const onPaste = () => {
    try {
      load(parsePastedCsv(paste), null);
      setPaste('');
    } catch (err) {
      setError(describe(err));
    }
  };

  const changeMapping = (field: FacetImportField, value: string) => {
    setMapping((m) => {
      const next = { ...m };
      if (value === '') delete next[field];
      else next[field] = Number(value);
      return next;
    });
    reset();
  };

  const rowsToSend = useMemo(() => buildRows(dataRows, mapping, defaults), [dataRows, mapping, defaults]);
  const duplicates = useMemo(() => findDuplicates(rowsToSend), [rowsToSend]);
  const preview = rowsToSend.slice(0, PREVIEW_ROWS);
  const noFamily = mapping.memberType === undefined && !fileType;
  const noKey = mapping.key === undefined && !fileKey.trim();
  const mappingProblem =
    mapping.token === undefined
      ? t('importNeedsToken')
      : mapping.value === undefined
        ? t('importNeedsValue')
        : noFamily
          ? t('importNeedsFamily')
          : noKey
            ? t('importNeedsKey')
            : null;

  const run = async (dryRun: boolean, only?: Set<number>) => {
    const controller = new AbortController();
    ctl.current = controller;
    const base = only ? outcomes : [];
    setError(null);
    setNotice(null);
    setProgress(0);
    if (!only) setOutcomes([]);
    setFilter('ALL');
    setPhase('running');
    const finish = (got: FileRowOutcome[]) => (only ? mergeOutcomes(base, got) : got);
    try {
      const { outcomes: got, stopped } = await runFacetImport(
        rowsToSend,
        dryRun,
        (rows, dry) => importFacetValues(rows, dry),
        {
          only,
          signal: controller.signal,
          onProgress: (n, o) => {
            setProgress(n);
            setOutcomes(finish(o));
          },
        },
      );
      const all = finish(got);
      setOutcomes(all);
      if (dryRun) {
        setPhase(stopped ? 'map' : 'checked');
      } else {
        if (stopped) setNotice(t('importStopped', { count: got.length }));
        // Nothing secret is kept once the run is over; the table stays for the failed-rows file.
        setTable((tb) => (tb ? stripSecretColumns(tb) : tb));
        setPhase('done');
      }
    } catch (err) {
      if (err instanceof ImportRunError) {
        setOutcomes(finish(err.outcomes));
        setError(t('importChunkFailed', { count: err.outcomes.length, error: errMessage(err.cause) }));
        if (!dryRun) setTable((tb) => (tb ? stripSecretColumns(tb) : tb));
        setPhase(dryRun ? 'map' : 'done');
      } else {
        setError(errMessage(err));
        setPhase('map');
      }
    } finally {
      if (ctl.current === controller) ctl.current = null;
    }
  };

  // Rows worth sending again: the ones the server refused (a transient failure) and the ones
  // never answered (a stopped run or a failed chunk). An invalid row needs a corrected file.
  const retryRows = useMemo(() => {
    const answered = new Set(outcomes.map((o) => o.row));
    const rows = new Set<number>();
    outcomes.forEach((o) => {
      if (RETRYABLE_OUTCOMES.includes(o.outcome) && !duplicates.has(o.row)) rows.add(o.row);
    });
    rowsToSend.forEach((_, i) => {
      if (!answered.has(i + 1)) rows.add(i + 1);
    });
    return rows;
  }, [outcomes, rowsToSend, duplicates]);

  const counts = countOutcomes(outcomes);
  const failedCount = counts.INVALID + counts.REFUSED;
  const shown = (filter === 'ALL' ? outcomes : outcomes.filter((o) => o.outcome === filter)).slice(0, RENDER_CAP);
  const filteredTotal = filter === 'ALL' ? outcomes.length : counts[filter];

  const detail = (o: FileRowOutcome): string => {
    if (o.code === 'DUPLICATE_IN_FILE') return t('importCode_DUPLICATE_IN_FILE');
    if (o.code === 'NO_RESULT') return t('importCode_NO_RESULT');
    if (o.code === 'RESPONSE_LOST') return t('importCode_RESPONSE_LOST');
    return o.message ?? '—';
  };

  const resultsTable = (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2" role="group" aria-label={t('importFilterLabel')}>
        <Button
          size="sm"
          variant={filter === 'ALL' ? 'default' : 'outline'}
          aria-pressed={filter === 'ALL'}
          onClick={() => setFilter('ALL')}
        >
          {t('importFilterAll', { count: outcomes.length })}
        </Button>
        {OUTCOMES.filter((o) => counts[o] > 0).map((o) => (
          <Button
            key={o}
            size="sm"
            variant={filter === o ? 'default' : 'outline'}
            aria-pressed={filter === o}
            onClick={() => setFilter(o)}
          >
            {t(`importOutcome_${o}`)} · {counts[o]}
          </Button>
        ))}
      </div>
      <DataTable>
        <DataTableHead>
          <DataTableHeaderCell>{t('importColRow')}</DataTableHeaderCell>
          <DataTableHeaderCell>{t('colMember')}</DataTableHeaderCell>
          <DataTableHeaderCell>{t('importColToken')}</DataTableHeaderCell>
          <DataTableHeaderCell>{t('colKey')}</DataTableHeaderCell>
          <DataTableHeaderCell>{t('importColOutcome')}</DataTableHeaderCell>
          <DataTableHeaderCell>{t('importColMessage')}</DataTableHeaderCell>
        </DataTableHead>
        <DataTableBody>
          {shown.map((o) => (
            <DataTableRow key={o.row}>
              <DataTableCell className="tabular-nums text-muted-foreground">{o.row}</DataTableCell>
              <DataTableCell className="text-muted-foreground">{familyLabel(o.memberType)}</DataTableCell>
              <DataTableCell>
                <span className="font-mono text-xs">{o.token}</span>
              </DataTableCell>
              <DataTableCell>
                <span className="font-mono text-xs">{o.key}</span>
              </DataTableCell>
              <DataTableCell>
                <Badge variant={outcomeVariant(o.outcome)}>{t(`importOutcome_${o.outcome}`)}</Badge>
              </DataTableCell>
              <DataTableCell className="text-muted-foreground">{detail(o)}</DataTableCell>
            </DataTableRow>
          ))}
        </DataTableBody>
      </DataTable>
      {filteredTotal > RENDER_CAP && (
        <p className="text-xs text-muted-foreground">
          {t('importShowingFirst', { shown: RENDER_CAP, total: filteredTotal })}
        </p>
      )}
    </div>
  );

  const downloads = (
    <div className="flex flex-wrap gap-2">
      <Button
        variant="outline"
        size="sm"
        onClick={() => downloadCsv(exportFilename(RESULTS_STEM), resultsCsv(outcomes, secretFields(header, mapping)))}
      >
        <Download size={16} /> {t('importDownloadResults')}
      </Button>
      {outcomes.some((o) => FAILED_OUTCOMES.includes(o.outcome)) && table && (
        <Button
          variant="outline"
          size="sm"
          onClick={() =>
            downloadCsv(exportFilename(FAILED_STEM), failedRowsCsv(dataRows, header, mapping, defaults, outcomes))
          }
        >
          <Download size={16} /> {t('importDownloadFailed')}
        </Button>
      )}
    </div>
  );

  return (
    <div className="space-y-5">
      {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
      {notice && (
        <p role="status" className="text-sm text-muted-foreground">
          {notice}
        </p>
      )}

      {phase === 'pick' && (
        <div className="space-y-3">
          <p className="text-sm text-muted-foreground">
            {t('importPickHint', { maxMb: IMPORT_MAX_BYTES / (1024 * 1024), maxRows: IMPORT_MAX_ROWS })}
          </p>
          <FilePicker onPick={(f) => void onFile(f)} accept=".csv,.tsv,.txt,text/csv,text/plain">
            <FileUp size={16} /> {t('importChooseFile')}
          </FilePicker>
          <FormField label={t('importPasteLabel')} htmlFor="fimp-paste">
            <Textarea
              id="fimp-paste"
              rows={6}
              value={paste}
              onChange={(e) => setPaste(e.target.value)}
              placeholder={t('importPastePlaceholder')}
              spellCheck={false}
            />
          </FormField>
          <Button variant="outline" size="sm" disabled={!paste.trim()} onClick={onPaste}>
            {t('importUsePasted')}
          </Button>
        </div>
      )}

      {table && phase !== 'pick' && (
        <>
          <p className="text-sm text-muted-foreground">
            {t('importFileSummary', { name: fileName ?? t('importPastedName'), count: dataRows.length })}
          </p>

          {(phase === 'map' || phase === 'checked') && (
            <div className="space-y-3">
              <h3 className="text-sm font-medium">{t('importMappingTitle')}</h3>
              {FACET_IMPORT_FIELDS.map((f) => (
                <FormField key={f} label={t(`importField_${f}`)} htmlFor={`fimp-map-${f}`}>
                  <Combobox
                    id={`fimp-map-${f}`}
                    options={columnOptions}
                    value={mapping[f] === undefined ? '' : String(mapping[f])}
                    onChange={(v) => changeMapping(f, v)}
                    placeholder={t('importNotMapped')}
                  />
                </FormField>
              ))}
              <FormField label={t('importFileFamilyLabel')} htmlFor="fimp-family" description={t('importFileFamilyHint')}>
                <Combobox
                  id="fimp-family"
                  options={familyOptions}
                  value={fileType}
                  onChange={(v) => {
                    setFileType(v);
                    reset();
                  }}
                  placeholder={t('importFileFamilyPlaceholder')}
                />
              </FormField>
              <FormField label={t('importFileKeyLabel')} htmlFor="fimp-key" description={t('importFileKeyHint')}>
                <Input
                  id="fimp-key"
                  value={fileKey}
                  onChange={(e) => {
                    setFileKey(e.target.value);
                    reset();
                  }}
                  placeholder={t('importFileKeyPlaceholder')}
                />
              </FormField>

              {preview.length > 0 && (
                <div className="space-y-1">
                  <h3 className="text-sm font-medium">{t('importPreviewTitle')}</h3>
                  <DataTable>
                    <DataTableHead>
                      <DataTableHeaderCell>{t('colMember')}</DataTableHeaderCell>
                      <DataTableHeaderCell>{t('importColToken')}</DataTableHeaderCell>
                      <DataTableHeaderCell>{t('colKey')}</DataTableHeaderCell>
                      <DataTableHeaderCell>{t('colValue')}</DataTableHeaderCell>
                    </DataTableHead>
                    <DataTableBody>
                      {preview.map((r, i) => (
                        <DataTableRow key={i}>
                          <DataTableCell>{r.memberType ? familyLabel(r.memberType) : '—'}</DataTableCell>
                          <DataTableCell>
                            <span className="font-mono text-xs">{r.token || '—'}</span>
                          </DataTableCell>
                          <DataTableCell>
                            <span className="font-mono text-xs">{r.key || '—'}</span>
                          </DataTableCell>
                          <DataTableCell className="max-w-48 truncate">{r.value || '—'}</DataTableCell>
                        </DataTableRow>
                      ))}
                    </DataTableBody>
                  </DataTable>
                </div>
              )}
              {duplicates.size > 0 && (
                <p role="status" className="text-sm text-amber-600">
                  {t('importDuplicatesWarning', { count: duplicates.size })}
                </p>
              )}
              {mappingProblem && <p className="text-sm text-destructive">{mappingProblem}</p>}
            </div>
          )}

          {phase === 'map' && (
            <div className="flex gap-2">
              <Button disabled={!!mappingProblem || dataRows.length === 0} onClick={() => void run(true)}>
                {t('importCheckFile')}
              </Button>
              <Button
                variant="outline"
                onClick={() => {
                  setTable(null);
                  setPhase(PHASE_PICK);
                }}
              >
                {t('importChooseAnother')}
              </Button>
            </div>
          )}

          {phase === 'checked' && (
            <div className="space-y-3">
              <h3 className="text-sm font-medium">{t('importCheckResultTitle')}</h3>
              {resultsTable}
              <div className="flex flex-wrap items-center gap-2">
                <Button disabled={counts.SET === 0} onClick={() => void run(false)}>
                  {t('importStart', { count: counts.SET })}
                </Button>
                {downloads}
              </div>
              {counts.SET === 0 && <p className="text-sm text-muted-foreground">{t('importNothingToSet')}</p>}
            </div>
          )}

          {phase === 'running' && (
            <div className="space-y-3">
              <p role="status" className="text-sm tabular-nums">
                {t('importProgress', { done: progress, total: rowsToSend.length })}
              </p>
              <progress
                className="h-2 w-full"
                value={progress}
                max={Math.max(rowsToSend.length, 1)}
                aria-label={t('importProgressLabel')}
              />
              <Button variant="outline" onClick={() => ctl.current?.abort()}>
                {t('importStop')}
              </Button>
            </div>
          )}

          {phase === 'done' && (
            <div className="space-y-3">
              <h3 className="text-sm font-medium">{t('importDoneTitle')}</h3>
              <p className="text-sm" data-shot="import-summary">
                {t('importSummary', { set: counts.SET, exists: counts.EXISTS, failed: failedCount })}
              </p>
              {counts.UNKNOWN > 0 && (
                <p role="status" className="text-sm text-amber-600">
                  {t('importUnknownNote', { count: counts.UNKNOWN })}
                </p>
              )}
              {resultsTable}
              <div className="flex flex-wrap items-center gap-2">
                {retryRows.size > 0 && (
                  <Button variant="outline" onClick={() => void run(false, retryRows)}>
                    {t('importRetry', { count: retryRows.size })}
                  </Button>
                )}
                {downloads}
                <Button onClick={() => onDone(t('importDoneToast', { count: counts.SET }))}>{t('importClose')}</Button>
              </div>
              {failedCount > 0 && <p className="text-xs text-muted-foreground">{t('importRerunHint')}</p>}
            </div>
          )}
        </>
      )}
    </div>
  );
}

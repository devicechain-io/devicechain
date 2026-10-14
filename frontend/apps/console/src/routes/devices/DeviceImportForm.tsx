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
import { SegmentedControl } from '@/components/ui/segmented-control';
import { Textarea } from '@/components/ui/textarea';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage } from '@/routes/common';
import {
  importDevices,
  listDeviceTypes,
  type DeviceImportMode,
  type DeviceImportOutcome,
} from '@/lib/api/device-management';
import { downloadCsv, exportFilename } from '@/lib/csv';
import {
  CsvImportError,
  IMPORT_MAX_BYTES,
  IMPORT_MAX_ROWS,
  parsePastedCsv,
  readCsvFile,
} from '@/lib/csvImport';
import {
  FAILED_OUTCOMES,
  ImportRunError,
  autoMap,
  buildRows,
  countOutcomes,
  failedRowsCsv,
  fieldsForMode,
  resultsCsv,
  runImport,
  stripSecrets,
  type ColumnMapping,
  type FileRowOutcome,
  type ImportField,
} from '@/routes/devices/deviceImport';

// The most outcome rows drawn at once. A file can hold 10 000; the full list is the download.
const RESULTS_STEM = 'devices-import-results';
const FAILED_STEM = 'devices-import-failed';
const PHASE_MAP: Phase = 'map';
const PHASE_PICK: Phase = 'pick';
const RENDER_CAP = 200;
const PREVIEW_ROWS = 3;

type Phase = 'pick' | 'map' | 'checked' | 'running' | 'done';
type Filter = 'ALL' | DeviceImportOutcome;

const OUTCOMES: DeviceImportOutcome[] = ['CREATED', 'EXISTS', 'CONFLICT', 'INVALID', 'REFUSED'];
const MODES: DeviceImportMode[] = ['NONE', 'FACTORY_CREDENTIALS'];

const outcomeVariant = (o: DeviceImportOutcome) =>
  o === 'CREATED' || o === 'EXISTS' ? 'success' : o === 'CONFLICT' ? 'warning' : 'destructive';

// DeviceImportForm imports devices from a CSV file in four steps: read the file and choose how
// credentials are handled, map its columns, preview the result with a dry run (the server
// decides every row exactly as the real import will), then import in sequential chunks. A
// finished import can be re-run safely — the device token is the key, so rows that landed
// report "exists". Credential secrets live only in this component's state, are never put in
// the results, and are blanked when the import ends.
export function DeviceImportForm({ onDone }: { onDone: (message: string) => void }) {
  const { t } = useTranslation('devices');
  const { data: types } = useQuery(
    () => listDeviceTypes({ pageNumber: 1, pageSize: 1000 }).then((r) => r.results),
    [],
  );
  const typeOptions: ComboboxOption[] = (types ?? []).map((ty) => ({ value: ty.token, label: ty.name || ty.token }));

  const [mode, setMode] = useState<DeviceImportMode>('NONE');
  const [paste, setPaste] = useState('');
  const [fileName, setFileName] = useState<string | null>(null);
  const [table, setTable] = useState<string[][] | null>(null);
  const [mapping, setMapping] = useState<ColumnMapping>({});
  const [fileType, setFileType] = useState('');
  const [phase, setPhase] = useState<Phase>('pick');
  const [outcomes, setOutcomes] = useState<FileRowOutcome[]>([]);
  const [progress, setProgress] = useState(0);
  const [filter, setFilter] = useState<Filter>('ALL');
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const ctl = useRef<AbortController | null>(null);

  // Closing the drawer abandons a run between chunks; whatever was answered stands on the server.
  useEffect(() => () => ctl.current?.abort(), []);

  const header = table?.[0] ?? [];
  const dataRows = useMemo(() => (table ? table.slice(1) : []), [table]);
  const fields = fieldsForMode(mode);
  const columnOptions: ComboboxOption[] = header.map((h, i) => ({
    value: String(i),
    label: `${i + 1}. ${h.trim() || t('importBlankColumn')}`,
  }));

  const describe = (err: unknown): string => {
    if (err instanceof CsvImportError) {
      return t(`importErr_${err.code}`, {
        maxMb: IMPORT_MAX_BYTES / (1024 * 1024),
        maxRows: IMPORT_MAX_ROWS,
      });
    }
    return errMessage(err);
  };

  const load = (rows: string[][], name: string | null) => {
    setError(null);
    setNotice(null);
    setTable(rows);
    setFileName(name);
    setMapping(autoMap(rows[0], mode));
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

  const changeMode = (next: DeviceImportMode) => {
    setMode(next);
    if (table) {
      setMapping(autoMap(table[0], next));
      setOutcomes([]);
      setPhase('map');
    }
  };

  const changeMapping = (field: ImportField, value: string) => {
    setMapping((m) => {
      const next = { ...m };
      if (value === '') delete next[field];
      else next[field] = Number(value);
      return next;
    });
    setOutcomes([]);
    setPhase('map');
  };

  const rowsToSend = useMemo(
    () => buildRows(dataRows, mapping, mode, fileType),
    [dataRows, mapping, mode, fileType],
  );
  const preview = rowsToSend.slice(0, PREVIEW_ROWS);
  const mappingProblem =
    mapping.token === undefined
      ? t('importNeedsToken')
      : mapping.deviceTypeToken === undefined && !fileType
        ? t('importNeedsType')
        : mode === 'FACTORY_CREDENTIALS' && (mapping.credentialType === undefined || mapping.credentialId === undefined)
          ? t('importNeedsCredentialColumns')
          : null;

  const run = async (dryRun: boolean) => {
    const controller = new AbortController();
    ctl.current = controller;
    setError(null);
    setNotice(null);
    setProgress(0);
    setOutcomes([]);
    setFilter('ALL');
    setPhase('running');
    try {
      const { outcomes: got, stopped } = await runImport(
        rowsToSend,
        dryRun,
        (rows, dry) => importDevices(rows, mode, dry),
        { signal: controller.signal, onProgress: (n, o) => { setProgress(n); setOutcomes([...o]); } },
      );
      setOutcomes(got);
      if (dryRun) {
        setPhase(stopped ? 'map' : 'checked');
      } else {
        if (stopped) setNotice(t('importStopped', { count: got.length }));
        // The secrets are done with: keep the file for the failed-rows download, minus them.
        setTable((tb) => (tb ? stripSecrets(tb, mapping) : tb));
        setPhase('done');
      }
    } catch (err) {
      if (err instanceof ImportRunError) {
        setOutcomes(err.outcomes);
        setError(t('importChunkFailed', { count: err.outcomes.length, error: errMessage(err.cause) }));
        if (!dryRun) setTable((tb) => (tb ? stripSecrets(tb, mapping) : tb));
        setPhase(dryRun ? 'map' : 'done');
      } else {
        setError(errMessage(err));
        setPhase('map');
      }
    } finally {
      if (ctl.current === controller) ctl.current = null;
    }
  };

  const counts = countOutcomes(outcomes);
  const shown = (filter === 'ALL' ? outcomes : outcomes.filter((o) => o.outcome === filter)).slice(0, RENDER_CAP);
  const filteredTotal = filter === 'ALL' ? outcomes.length : counts[filter];

  const resultsTable = (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2" role="group" aria-label={t('importFilterLabel')}>
        <Button size="sm" variant={filter === 'ALL' ? 'default' : 'outline'} onClick={() => setFilter('ALL')}>
          {t('importFilterAll', { count: outcomes.length })}
        </Button>
        {OUTCOMES.filter((o) => counts[o] > 0).map((o) => (
          <Button key={o} size="sm" variant={filter === o ? 'default' : 'outline'} onClick={() => setFilter(o)}>
            {t(`importOutcome_${o}`)} · {counts[o]}
          </Button>
        ))}
      </div>
      <DataTable>
        <DataTableHead>
          <DataTableHeaderCell>{t('importColRow')}</DataTableHeaderCell>
          <DataTableHeaderCell>{t('common:colToken')}</DataTableHeaderCell>
          <DataTableHeaderCell>{t('importColOutcome')}</DataTableHeaderCell>
          <DataTableHeaderCell>{t('importColMessage')}</DataTableHeaderCell>
        </DataTableHead>
        <DataTableBody>
          {shown.map((o) => (
            <DataTableRow key={o.row}>
              <DataTableCell className="tabular-nums text-muted-foreground">{o.row}</DataTableCell>
              <DataTableCell>
                <span className="font-mono text-xs">{o.token}</span>
              </DataTableCell>
              <DataTableCell>
                <Badge variant={outcomeVariant(o.outcome)}>{t(`importOutcome_${o.outcome}`)}</Badge>
              </DataTableCell>
              <DataTableCell className="text-muted-foreground">{o.message ?? '—'}</DataTableCell>
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
        onClick={() => downloadCsv(exportFilename(RESULTS_STEM), resultsCsv(outcomes))}
      >
        <Download size={16} /> {t('importDownloadResults')}
      </Button>
      {outcomes.some((o) => FAILED_OUTCOMES.includes(o.outcome)) && table && (
        <Button
          variant="outline"
          size="sm"
          onClick={() =>
            downloadCsv(exportFilename(FAILED_STEM), failedRowsCsv(dataRows, mapping, mode, outcomes, fileType))
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

      <FormField label={t('importModeLabel')} htmlFor="imp-mode" description={t(`importModeHint_${mode}`)}>
        <SegmentedControl
          ariaLabel={t('importModeLabel')}
          tone="joined"
          size="sm"
          fill
          value={mode}
          disabled={phase === 'running'}
          onValueChange={changeMode}
          options={MODES.map((m) => ({ value: m, label: t(`importMode_${m}`) }))}
        />
      </FormField>

      {phase === 'pick' && (
        <div className="space-y-3">
          <p className="text-sm text-muted-foreground">
            {t('importPickHint', { maxMb: IMPORT_MAX_BYTES / (1024 * 1024), maxRows: IMPORT_MAX_ROWS })}
          </p>
          <FilePicker onPick={(f) => void onFile(f)} accept=".csv,.tsv,.txt,text/csv,text/plain">
            <FileUp size={16} /> {t('importChooseFile')}
          </FilePicker>
          <FormField label={t('importPasteLabel')} htmlFor="imp-paste">
            <Textarea
              id="imp-paste"
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
              {fields.map((f) => (
                <FormField key={f} label={t(`importField_${f}`)} htmlFor={`imp-map-${f}`}>
                  <Combobox
                    id={`imp-map-${f}`}
                    options={columnOptions}
                    value={mapping[f] === undefined ? '' : String(mapping[f])}
                    onChange={(v) => changeMapping(f, v)}
                    placeholder={t('importNotMapped')}
                  />
                </FormField>
              ))}
              <FormField label={t('importFileTypeLabel')} htmlFor="imp-type" description={t('importFileTypeHint')}>
                <Combobox
                  id="imp-type"
                  options={typeOptions}
                  value={fileType}
                  onChange={(v) => {
                    setFileType(v);
                    setOutcomes([]);
                    setPhase(PHASE_MAP);
                  }}
                  placeholder={t('importFileTypePlaceholder')}
                />
              </FormField>

              {preview.length > 0 && (
                <div className="space-y-1">
                  <h3 className="text-sm font-medium">{t('importPreviewTitle')}</h3>
                  <DataTable>
                    <DataTableHead>
                      <DataTableHeaderCell>{t('common:colToken')}</DataTableHeaderCell>
                      <DataTableHeaderCell>{t('importField_deviceTypeToken')}</DataTableHeaderCell>
                      <DataTableHeaderCell>{t('common:colName')}</DataTableHeaderCell>
                      <DataTableHeaderCell>{t('importField_externalId')}</DataTableHeaderCell>
                    </DataTableHead>
                    <DataTableBody>
                      {preview.map((r, i) => (
                        <DataTableRow key={i}>
                          <DataTableCell>
                            <span className="font-mono text-xs">{r.token || '—'}</span>
                          </DataTableCell>
                          <DataTableCell>{r.deviceTypeToken || '—'}</DataTableCell>
                          <DataTableCell>{r.name ?? '—'}</DataTableCell>
                          <DataTableCell>{r.externalId ?? '—'}</DataTableCell>
                        </DataTableRow>
                      ))}
                    </DataTableBody>
                  </DataTable>
                </div>
              )}
              {mode === 'FACTORY_CREDENTIALS' && (
                <p className="text-xs text-muted-foreground">{t('importSecretsNotice')}</p>
              )}
              {mappingProblem && <p className="text-sm text-destructive">{mappingProblem}</p>}
            </div>
          )}

          {phase === 'map' && (
            <div className="flex gap-2">
              <Button disabled={!!mappingProblem || dataRows.length === 0} onClick={() => void run(true)}>
                {t('importCheckFile')}
              </Button>
              <Button variant="outline" onClick={() => {
                  setTable(null);
                  setPhase(PHASE_PICK);
                }}>
                {t('importChooseAnother')}
              </Button>
            </div>
          )}

          {phase === 'checked' && (
            <div className="space-y-3">
              <h3 className="text-sm font-medium">{t('importCheckResultTitle')}</h3>
              {resultsTable}
              <div className="flex flex-wrap items-center gap-2">
                <Button disabled={counts.CREATED === 0} onClick={() => void run(false)}>
                  {t('importStart', { count: counts.CREATED })}
                </Button>
                {downloads}
              </div>
              {counts.CREATED === 0 && <p className="text-sm text-muted-foreground">{t('importNothingToCreate')}</p>}
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
                {t('importSummary', {
                  created: counts.CREATED,
                  exists: counts.EXISTS,
                  failed: counts.CONFLICT + counts.INVALID + counts.REFUSED,
                })}
              </p>
              {resultsTable}
              <div className="flex flex-wrap items-center gap-2">
                {downloads}
                <Button onClick={() => onDone(t('importDoneToast', { count: counts.CREATED }))}>{t('importClose')}</Button>
              </div>
              {counts.CONFLICT + counts.INVALID + counts.REFUSED > 0 && (
                <p className="text-xs text-muted-foreground">{t('importRerunHint')}</p>
              )}
            </div>
          )}
        </>
      )}
    </div>
  );
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The test panel: run the decoder as it is in the editor (saved or not) over a pasted frame, a JSON
// object, a whole webhook body from a stored integration, or a stored failure sample, through the
// same code that runs at ingest. Nothing is stored.
//
// A stored sample is offered only to a caller with device:write: running a decoder over a sample
// reveals its bytes, so the platform treats it as reading them. Hiding the option is a courtesy;
// the server refuses a read-only caller regardless.

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Play, Save } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { FormField } from '@/components/ui/form-field';
import { Badge } from '@/components/ui/badge';
import { Combobox } from '@/components/ui/combobox';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { Select, SelectItem } from '@/components/ui/select';
import { LoadingState } from '@/components/ui/loading-state';
import { DataTable, DataTableHead, DataTableHeaderCell, DataTableBody, DataTableRow, DataTableCell } from '@/components/ui/data-table';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage } from '@/routes/common';
import { errorCode } from '@devicechain/client';
import { listIntegrations } from '@/lib/api/integrations';
import { testPayloadDecoder, type PayloadDecoderTestInput, type PayloadDecoderTestResult } from '@/lib/api/payload-decoder';
import { EXAMPLE } from './examples';
import { blankVector, newId, type InputKind, type VectorState } from './model';
import '@/i18n/lazy/deviceProfiles';

export type TestMode = 'frame' | 'json' | 'webhook' | 'sample';

/** `Name: value` lines to header inputs. Blank lines are dropped; a line with no colon is a name with no value. */
export function parseHeaders(text: string): { name: string; value: string }[] {
  return text.split('\n').flatMap((line) => {
    const l = line.trim();
    if (l === '') return [];
    const i = l.indexOf(':');
    return [i < 0 ? { name: l, value: '' } : { name: l.slice(0, i).trim(), value: l.slice(i + 1).trim() }];
  });
}

/** `name=value` lines to query inputs. */
export function parseQuery(text: string): { name: string; value: string }[] {
  return text.split('\n').flatMap((line) => {
    const l = line.trim();
    if (l === '') return [];
    const i = l.indexOf('=');
    return [i < 0 ? { name: l, value: '' } : { name: l.slice(0, i).trim(), value: l.slice(i + 1).trim() }];
  });
}

export interface TestRun {
  /** The editor's document text this result was produced from. */
  documentText: string;
  result: PayloadDecoderTestResult;
}

interface Props {
  profileToken: string;
  /** The document in the editor, or null while the form has problems that stop it being built. */
  documentText: string | null;
  inputKind: InputKind;
  canWriteSamples: boolean;
  /** The decoder's static output count, from the form. */
  maxReadings: number;
  /** The last run, held by the editor so the highlights and the vectors list can share it. */
  run: TestRun | null;
  onRun: (run: TestRun) => void;
  onSaveVector: (v: VectorState) => void;
  /** Set to load a stored failure sample into the panel (the recent-failures list). */
  initialSampleId?: string;
  /** The text of the expression the last error's path points at, shown beside the path. */
  errorExpression?: string | null;
}

function WebhookInputs(props: {
  integration: string;
  setIntegration: (v: string) => void;
  body: string;
  setBody: (v: string) => void;
  headers: string;
  setHeaders: (v: string) => void;
  query: string;
  setQuery: (v: string) => void;
  messageType: string;
  setMessageType: (v: string) => void;
  contentType: string;
  setContentType: (v: string) => void;
}) {
  const { t } = useTranslation('deviceProfiles');
  const { data, loading, error } = useQuery(() => listIntegrations({ pageNumber: 1, pageSize: 200 }), []);
  const options = (data?.results ?? []).map((i) => ({ value: i.token, label: i.name || i.token, description: `${i.type} · ${i.token}` }));
  return (
    <div className="space-y-3">
      <FormField label={t('decTestIntegration')} htmlFor="dec-test-integration" description={t('decTestIntegrationHint')}>
        {loading && !data ? (
          <LoadingState description={t('decTestIntegrationLoading')} />
        ) : error && !data ? (
          <p role="alert" className="text-sm text-destructive">{error}</p>
        ) : (
          <Combobox
            id="dec-test-integration"
            options={options}
            value={props.integration}
            onChange={props.setIntegration}
            allowClear={false}
            placeholder={t('decTestIntegrationPlaceholder')}
            emptyMessage={t('decTestIntegrationNone')}
          />
        )}
      </FormField>
      <FormField label={t('decTestWebhookBody')} htmlFor="dec-test-body">
        <Textarea id="dec-test-body" rows={8} value={props.body} onChange={(e) => props.setBody(e.target.value)} spellCheck={false} />
      </FormField>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <FormField label={t('decTestHeaders')} htmlFor="dec-test-headers" description={t('decTestHeadersHint')}>
          <Textarea id="dec-test-headers" rows={3} value={props.headers} onChange={(e) => props.setHeaders(e.target.value)} spellCheck={false} />
        </FormField>
        <FormField label={t('decTestQuery')} htmlFor="dec-test-query" description={t('decTestQueryHint')}>
          <Textarea id="dec-test-query" rows={3} value={props.query} onChange={(e) => props.setQuery(e.target.value)} spellCheck={false} />
        </FormField>
        <FormField label={t('decTestMessageType')} htmlFor="dec-test-mt">
          <Input id="dec-test-mt" value={props.messageType} onChange={(e) => props.setMessageType(e.target.value)} />
        </FormField>
        <FormField label={t('decTestContentType')} htmlFor="dec-test-ct">
          <Input id="dec-test-ct" value={props.contentType} onChange={(e) => props.setContentType(e.target.value)} placeholder={EXAMPLE.contentType} />
        </FormField>
      </div>
    </div>
  );
}

export function TestPanel({ profileToken, documentText, inputKind, canWriteSamples, maxReadings, run, onRun, onSaveVector, initialSampleId, errorExpression }: Props) {
  const { t } = useTranslation('deviceProfiles');
  const [mode, setMode] = useState<TestMode>(initialSampleId && canWriteSamples ? 'sample' : inputKind === 'FRAME' ? 'frame' : 'json');
  const [enc, setEnc] = useState<'hex' | 'base64'>('hex');
  const [frame, setFrame] = useState('');
  const [fPort, setFPort] = useState('');
  const [fCnt, setFCnt] = useState('');
  const [jsonText, setJsonText] = useState('');
  const [integration, setIntegration] = useState('');
  const [body, setBody] = useState('');
  const [headers, setHeaders] = useState('');
  const [query, setQuery] = useState('');
  const [messageType, setMessageType] = useState('');
  const [contentType, setContentType] = useState('application/json');
  const [sampleId, setSampleId] = useState(initialSampleId ?? '');
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  // What the last run was made from, so "save as vector" cannot save a different input than was tested.
  const [ranWith, setRanWith] = useState<{ mode: TestMode; enc: 'hex' | 'base64'; frame: string; json: string; fPort: string; fCnt: string } | null>(null);

  const modes = [
    { value: 'frame' as const, label: t('decTestModeFrame') },
    { value: 'json' as const, label: t('decTestModeJson') },
    { value: 'webhook' as const, label: t('decTestModeWebhook') },
    ...(canWriteSamples ? [{ value: 'sample' as const, label: t('decTestModeSample') }] : []),
  ];

  const hasInput =
    (mode === 'frame' && frame.trim() !== '') ||
    (mode === 'json' && jsonText.trim() !== '') ||
    (mode === 'webhook' && body.trim() !== '' && integration !== '') ||
    (mode === 'sample' && /^\d+$/.test(sampleId.trim()));

  const intOrNull = (s: string): number | null => (/^\d+$/.test(s.trim()) ? Number(s.trim()) : null);

  async function go() {
    if (!documentText) return;
    setBusy(true);
    setFailure(null);
    const input: PayloadDecoderTestInput = { profileToken, document: documentText };
    if (mode === 'frame') {
      if (enc === 'hex') input.frameHex = frame;
      else input.frameBase64 = frame;
      const p = intOrNull(fPort);
      const c = intOrNull(fCnt);
      if (p !== null) input.fPort = p;
      if (c !== null) input.fCnt = c;
    } else if (mode === 'json') {
      input.json = jsonText;
    } else if (mode === 'webhook') {
      input.webhookBody = body;
      input.integrationToken = integration;
      const h = parseHeaders(headers);
      const q = parseQuery(query);
      if (h.length > 0) input.headers = h;
      if (q.length > 0) input.query = q;
      if (messageType.trim() !== '') input.messageType = messageType.trim();
      if (contentType.trim() !== '') input.contentType = contentType.trim();
    } else {
      input.sampleId = sampleId.trim();
    }
    try {
      const result = await testPayloadDecoder(input);
      setRanWith({ mode, enc, frame, json: jsonText, fPort, fCnt });
      onRun({ documentText, result });
    } catch (err) {
      setFailure(errorCode(err) === 'RATE_LIMITED' ? t('decTestRateLimited') : errMessage(err));
    } finally {
      setBusy(false);
    }
  }

  const result = run?.result ?? null;
  const stale = run !== null && run.documentText !== documentText;
  const canSaveVector = canWriteSamples && result?.ok === true && ranWith !== null && (ranWith.mode === 'frame' || ranWith.mode === 'json');

  function saveVector() {
    if (!result || !ranWith) return;
    const v = blankVector();
    if (ranWith.mode === 'frame') {
      v.kind = ranWith.enc === 'hex' ? 'frameHex' : 'frameBase64';
      v.text = ranWith.frame.trim();
      v.fPort = ranWith.fPort.trim();
      v.fCnt = ranWith.fCnt.trim();
    } else {
      v.kind = inputKind === 'DECODED' ? 'decoded' : 'json';
      v.text = ranWith.json.trim();
    }
    v.expect = result.readings.flatMap((r) => (Number.isFinite(Number(r.value)) ? [{ id: newId(), key: r.key, value: r.value }] : []));
    onSaveVector(v);
  }

  return (
    <div className="space-y-4">
      <SegmentedControl ariaLabel={t('decTestModeLabel')} options={modes} value={mode} onValueChange={setMode} tone="inset" size="sm" className="flex-wrap" />

      {mode === 'frame' && (
        <div className="space-y-3">
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-4">
            <FormField label={t('decTestEncoding')} htmlFor="dec-test-enc">
              <Select id="dec-test-enc" value={enc} onChange={(v) => setEnc(v as 'hex' | 'base64')}>
                <SelectItem value="hex">{t('decTestHex')}</SelectItem>
                <SelectItem value="base64">{t('decTestBase64')}</SelectItem>
              </Select>
            </FormField>
            <FormField label={t('decFPort')} htmlFor="dec-test-fport">
              <Input id="dec-test-fport" inputMode="numeric" value={fPort} onChange={(e) => setFPort(e.target.value)} />
            </FormField>
            <FormField label={t('decTestFCnt')} htmlFor="dec-test-fcnt">
              <Input id="dec-test-fcnt" inputMode="numeric" value={fCnt} onChange={(e) => setFCnt(e.target.value)} />
            </FormField>
          </div>
          <FormField label={t('decTestFrame')} htmlFor="dec-test-frame" description={t('decTestFrameHint')}>
            <Textarea id="dec-test-frame" rows={3} value={frame} onChange={(e) => setFrame(e.target.value)} spellCheck={false} placeholder={EXAMPLE.frameHex} />
          </FormField>
        </div>
      )}
      {mode === 'json' && (
        <FormField label={t('decTestJson')} htmlFor="dec-test-json" description={t('decTestJsonHint')}>
          <Textarea id="dec-test-json" rows={8} value={jsonText} onChange={(e) => setJsonText(e.target.value)} spellCheck={false} />
        </FormField>
      )}
      {mode === 'webhook' && (
        <WebhookInputs
          integration={integration} setIntegration={setIntegration}
          body={body} setBody={setBody}
          headers={headers} setHeaders={setHeaders}
          query={query} setQuery={setQuery}
          messageType={messageType} setMessageType={setMessageType}
          contentType={contentType} setContentType={setContentType}
        />
      )}
      {mode === 'sample' && (
        <FormField label={t('decTestSampleId')} htmlFor="dec-test-sample" description={t('decTestSampleHint')}>
          <Input id="dec-test-sample" inputMode="numeric" value={sampleId} onChange={(e) => setSampleId(e.target.value)} />
        </FormField>
      )}

      <div className="flex flex-wrap items-center gap-3">
        <Button onClick={go} loading={busy} disabled={busy || !documentText || !hasInput}>
          <Play size={14} /> {t('decTestRun')}
        </Button>
        {!documentText && <span className="text-sm text-muted-foreground">{t('decTestFixFirst')}</span>}
        {canSaveVector && (
          <Button variant="outline" onClick={saveVector}>
            <Save size={14} /> {t('decTestSaveVector')}
          </Button>
        )}
      </div>

      <div aria-live="polite" className="space-y-3">
        {failure && (
          <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
            {failure}
          </p>
        )}
        {result && (
          <div className="space-y-3" data-testid="test-result">
            {stale && <p className="text-xs text-muted-foreground">{t('decTestStale')}</p>}
            <div className="flex flex-wrap items-center gap-2">
              {result.error ? (
                <Badge variant="destructive">{t('decTestFailed')}</Badge>
              ) : result.ignored ? (
                <Badge variant="secondary">{t('decTestIgnored')}</Badge>
              ) : (
                <Badge variant="success">{t('decTestOk')}</Badge>
              )}
              {result.section && <span className="text-sm text-muted-foreground">{t('decTestSection', { name: result.section })}</span>}
              <span className="text-sm text-muted-foreground tabular-nums">{t('decTestCost', { cost: result.cost })}</span>
              <span className="text-sm text-muted-foreground tabular-nums">
                {t('decTestReadings', { count: result.readings.length, max: maxReadings })}
              </span>
              {result.uplinks > 1 && <span className="text-sm text-muted-foreground">{t('decTestUplinks', { count: result.uplinks })}</span>}
            </div>

            {result.error && (
              <div role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 p-3 text-sm">
                <p className="font-medium text-destructive">{t(`decReason.${result.error.reason}`, { defaultValue: result.error.reason })}</p>
                <p className="mt-1">{result.error.message}</p>
                {result.error.path && (
                  <p className="mt-1 text-xs text-muted-foreground">
                    {t('decTestPath')}: <code className="font-mono">{result.error.path}</code>
                  </p>
                )}
                {errorExpression && (
                  <pre className="mt-1 overflow-x-auto rounded bg-background/60 p-2 font-mono text-xs ring-1 ring-destructive" data-testid="error-expression">
                    {errorExpression}
                  </pre>
                )}
                {result.error.offset != null && (
                  <p className="text-xs text-muted-foreground">{t('decTestOffset', { offset: result.error.offset })}</p>
                )}
              </div>
            )}

            {result.readings.length > 0 && (
              <DataTable>
                <DataTableHead>
                  <DataTableHeaderCell>{t('decTestKey')}</DataTableHeaderCell>
                  <DataTableHeaderCell>{t('decTestValue')}</DataTableHeaderCell>
                </DataTableHead>
                <DataTableBody>
                  {result.readings.map((r) => (
                    <DataTableRow key={r.key}>
                      <DataTableCell className="font-mono text-xs">{r.key}</DataTableCell>
                      <DataTableCell className="tabular-nums">{r.value}</DataTableCell>
                    </DataTableRow>
                  ))}
                </DataTableBody>
              </DataTable>
            )}
            {result.skipped.length > 0 && (
              <div>
                <p className="mb-1 text-sm font-medium">{t('decTestSkipped')}</p>
                <ul className="space-y-0.5 text-sm text-muted-foreground">
                  {result.skipped.map((s, n) => (
                    <li key={`${s.key}${n}`}>
                      <code className="font-mono text-xs">{s.key}</code> — {t(`decSkipReason.${s.reason.split(':')[0]}`, { defaultValue: s.reason })}
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

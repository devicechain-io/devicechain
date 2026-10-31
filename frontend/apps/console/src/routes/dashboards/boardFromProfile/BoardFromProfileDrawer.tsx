// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// "Create dashboard" for a device type: read what its profile declares, generate a board from
// existing widgets, show the layout, and on confirm create it as a DRAFT and open it in the
// editor. Nothing is published here, and the generated text goes through the same import
// pipeline as a pasted file (parser, option validation) before it is written.

import { useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { FormDrawer } from '@/components/registry';
import { Button } from '@/components/ui/button';
import { ErrorBanner } from '@/components/ui/error-banner';
import { FormField } from '@/components/ui/form-field';
import { Input } from '@/components/ui/input';
import { TokenField } from '@/components/ui/token-field';
import { useToast } from '@/components/ui/toast';
import { useQuery } from '@/lib/hooks/use-query';
import { createDashboard, getDashboard } from '@/lib/api/dashboards';
import { errMessage } from '@/routes/common';
import { defaultWidgetRegistry } from '../editor/widgetRegistry';
import { buildImportPayload, prepareImport } from '../import/importDefinition';
import { generateBoard, type BoardLabels } from './generate';
import { loadBoardSource } from './source';
import '@/i18n/lazy/dashboards';

export function BoardFromProfileDrawer({
  open,
  onOpenChange,
  typeToken,
  typeName,
  profileToken,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  typeToken: string;
  typeName: string;
  profileToken: string;
}) {
  const { t } = useTranslation('dashboards');
  return (
    <FormDrawer
      shot="board-from-profile"
      open={open}
      onOpenChange={onOpenChange}
      title={t('boardFromProfileTitle')}
      description={t('boardFromProfileIntro')}
    >
      {/* Mounted only while open and keyed by the type and profile it reads, so a half-finished
          form never carries from one type's page to another's. */}
      {open && (
        <BoardFlow
          key={`${typeToken}/${profileToken}`}
          typeToken={typeToken}
          typeName={typeName}
          profileToken={profileToken}
        />
      )}
    </FormDrawer>
  );
}

function BoardFlow({
  typeToken,
  typeName,
  profileToken,
}: {
  typeToken: string;
  typeName: string;
  profileToken: string;
}) {
  const { t } = useTranslation('dashboards');
  const navigate = useNavigate();
  const { toast } = useToast();
  const source = useQuery(() => loadBoardSource(typeToken, profileToken), [typeToken, profileToken]);
  const [name, setName] = useState(t('boardFromProfileDefaultName', { name: typeName }));
  const [token, setToken] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const labels: BoardLabels = useMemo(
    () => ({
      device: t('boardFromProfileDeviceLabel'),
      chart: t('boardFromProfileChartTitle'),
      alarms: t('boardFromProfileAlarmsTitle'),
      calculated: t('widgetMetricCalculated'),
      measurementsLeftOut: (shown, total) => t('boardFromProfileMeasurementsLeftOut', { shown, total }),
      commandsLeftOut: (count) => t('boardFromProfileCommandsLeftOut', { count }),
    }),
    [t],
  );

  // `current`, not `data`: the board is built from the answer to THIS type's question. data may
  // still be another type's measurements, and Create writes what is generated from it.
  const loaded = source.current;
  const board = useMemo(
    () =>
      loaded
        ? generateBoard({
            title: name.trim() || typeName,
            metrics: loaded.metrics,
            commands: loaded.commands,
            defaultDevice: loaded.defaultDevice,
            deviceType: typeToken,
            labels,
          })
        : null,
    [loaded, name, typeName, typeToken, labels],
  );

  const create = async () => {
    if (!board || !board.ok) return;
    setError(null);
    setBusy(true);
    try {
      const prepared = prepareImport(JSON.stringify(board.definition), defaultWidgetRegistry().catalog);
      if (prepared.unresolved.length > 0 || prepared.optionIssues.length > 0) {
        const first = prepared.optionIssues[0];
        throw new Error(
          t('boardFromProfileInvalid', {
            detail: first ? `${first.widgetId}: ${first.message}` : prepared.unresolved[0].widgetId,
          }),
        );
      }
      const created = await createDashboard({
        token: token.trim(),
        name: name.trim() || undefined,
        definition: buildImportPayload(prepared.definition, name),
      });
      toast(t('created', { token: created.token }));
      navigate(`/dashboards/${encodeURIComponent(created.token)}`);
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  if (source.loading && !loaded) return <p className="text-sm text-muted-foreground">{t('boardFromProfileLoading')}</p>;
  if (source.error || !loaded || !board) {
    return <ErrorBanner message={source.error ?? t('boardFromProfileLoadError')} />;
  }
  if (!board.ok) {
    return (
      <p role="alert" className="rounded-md border border-border p-3 text-sm">
        {t('boardFromProfileNoMeasurements')}
      </p>
    );
  }

  const { summary } = board;
  return (
    <div className="space-y-4">
      {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
      <FormField label={t('common:colName')} htmlFor="board-name">
        <Input id="board-name" value={name} onChange={(e) => setName(e.target.value)} />
      </FormField>
      <FormField label={t('common:colToken')} htmlFor="board-token" description={t('importTokenHint')}>
        <TokenField
          id="board-token"
          entityType="dashboard"
          value={token}
          onChange={setToken}
          seed={name}
          autoGenerate
          placeholder={t('createTokenPlaceholder')}
          checkAvailability={(candidate) => getDashboard(candidate).then((d) => d === null)}
        />
      </FormField>

      <div className="space-y-2" role="group" aria-label={t('boardFromProfilePreview')}>
        <p className="text-sm font-medium">{t('boardFromProfilePreview')}</p>
        <div
          className="grid gap-0.5 rounded-md border border-border p-1"
          style={{ gridTemplateColumns: 'repeat(12, minmax(0, 1fr))', gridAutoRows: '14px' }}
        >
          {summary.widgets.map((w) => (
            <div
              key={w.id}
              data-widget={w.type}
              title={w.title}
              className="truncate rounded-sm bg-muted px-1 text-[10px] leading-[14px] text-muted-foreground"
              style={{ gridColumn: `${w.col + 1} / span ${w.cols}`, gridRow: `${w.row + 1} / span ${w.rows}` }}
            >
              {w.title}
            </div>
          ))}
        </div>
        <ul className="list-disc pl-5 text-sm text-muted-foreground">
          <li>{t('boardFromProfileSummaryCards', { count: summary.cards })}</li>
          {summary.leftOut.length > 0 && (
            <li>
              {t('boardFromProfileMeasurementsLeftOut', {
                shown: summary.cards,
                total: summary.cards + summary.leftOut.length,
              })}
            </li>
          )}
          {summary.commands.length > 0 && (
            <li>{t('boardFromProfileSummaryCommands', { count: summary.commands.length })}</li>
          )}
          {summary.skippedCommands.length > 0 && (
            <li>{t('boardFromProfileCommandsLeftOut', { count: summary.skippedCommands.length })}</li>
          )}
          {!loaded.defaultDevice && <li>{t('boardFromProfileNoDevice')}</li>}
          {loaded.basis === 'draft' && <li>{t('boardFromProfileBasisDraft')}</li>}
        </ul>
      </div>

      <p className="text-sm text-muted-foreground">{t('importDraftNote')}</p>
      <Button onClick={() => void create()} loading={busy} disabled={busy || !token.trim()}>
        {t('boardFromProfileCreate')}
      </Button>
    </div>
  );
}

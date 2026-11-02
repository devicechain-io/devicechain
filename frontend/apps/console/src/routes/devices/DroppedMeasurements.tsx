// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useTranslation } from 'react-i18next';
import { AlertTriangle } from 'lucide-react';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import type { DroppedMeasurement } from '@/lib/api/device-state';
import { formatTime } from '@/lib/utils';

// The reasons the platform discards a measurement key, as the server names them. A reason this
// build does not know is shown as the server sent it rather than worded as one that it does:
// the table is a diagnostic, and a guess would hide the very disagreement worth seeing.
const REASON_KEYS: Record<string, string> = {
  UNDECLARED_NON_NUMERIC: 'droppedReason_UNDECLARED_NON_NUMERIC',
  UNDECLARED_UNSTORABLE: 'droppedReason_UNDECLARED_UNSTORABLE',
  DECLARED_NOT_STORABLE: 'droppedReason_DECLARED_NOT_STORABLE',
};

// The id the notice links to: the table lives on the Connectivity tab, beside the device's
// latest readings, and the notice switches to that tab.
export const DROPPED_TABLE_ID = 'dropped-measurements';

// DroppedMeasurementsNotice says, above every tab of the device page, that the platform is
// discarding some of what this device sends, and links to the table that says which keys.
//
// 🔴 IT SHOWS NOTHING WHEN IT CANNOT BE SURE. A failed read (a role without state:read), a
// device that loses nothing and a response that is not a list all render no notice: a banner
// asserting a loss the platform is not recording would be worse than a missing one, and a
// device that loses nothing is the ordinary case.
export function DroppedMeasurementsNotice({
  rows,
  onShowDetails,
}: {
  rows: DroppedMeasurement[] | null | undefined;
  onShowDetails: () => void;
}) {
  const { t } = useTranslation('devices');
  if (!Array.isArray(rows) || rows.length < 1) return null;
  return (
    <div
      role="status"
      data-testid="dropped-measurements-notice"
      className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-warning/40 bg-warning/10 px-4 py-3 text-sm"
    >
      <div className="space-y-0.5">
        <div className="flex items-center gap-2 font-medium text-foreground">
          <AlertTriangle size={16} className="text-warning" /> {t('droppedKeysNotice', { count: rows.length })}
        </div>
        <div className="text-muted-foreground">{t('droppedKeysHint')}</div>
      </div>
      <a
        href={`#${DROPPED_TABLE_ID}`}
        className="text-sm font-medium text-primary underline-offset-4 hover:underline"
        onClick={(e) => {
          e.preventDefault();
          onShowDetails();
        }}
      >
        {t('droppedKeysShowDetails')}
      </a>
    </div>
  );
}

// DroppedMeasurementsTable lists each discarded key: the key, why in words, the last value the
// device sent, how many times and when it last did. Renders nothing for an empty record.
export function DroppedMeasurementsTable({ rows }: { rows: DroppedMeasurement[] | null | undefined }) {
  const { t } = useTranslation('devices');
  if (!Array.isArray(rows) || rows.length < 1) return null;
  return (
    <div id={DROPPED_TABLE_ID} data-testid="dropped-measurements-table" className="space-y-2">
      <h3 className="text-sm font-medium text-foreground">{t('droppedKeysTitle')}</h3>
      <DataTable>
        <DataTableHead>
          <DataTableHeaderCell>{t('droppedKeyColumn')}</DataTableHeaderCell>
          <DataTableHeaderCell>{t('droppedReasonColumn')}</DataTableHeaderCell>
          <DataTableHeaderCell>{t('droppedLastValueColumn')}</DataTableHeaderCell>
          <DataTableHeaderCell>{t('droppedCountColumn')}</DataTableHeaderCell>
          <DataTableHeaderCell>{t('droppedLastSeenColumn')}</DataTableHeaderCell>
        </DataTableHead>
        <DataTableBody>
          {rows.map((d) => (
            <DataTableRow key={d.id}>
              <DataTableCell className="font-medium text-foreground">{d.name}</DataTableCell>
              <DataTableCell className="text-muted-foreground">
                {REASON_KEYS[d.reason] ? t(REASON_KEYS[d.reason]) : d.reason}
              </DataTableCell>
              <DataTableCell className="font-mono text-foreground" data-shot-mask>
                {d.lastValue}
              </DataTableCell>
              <DataTableCell className="text-foreground">{d.count}</DataTableCell>
              <DataTableCell className="text-muted-foreground" data-shot-mask>
                {formatTime(d.lastTime)}
              </DataTableCell>
            </DataTableRow>
          ))}
        </DataTableBody>
      </DataTable>
    </div>
  );
}

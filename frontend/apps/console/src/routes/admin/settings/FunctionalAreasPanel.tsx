// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useTranslation } from 'react-i18next';
import { Badge } from '@/components/ui/badge';
import { HintText } from '@/components/ui/hint-text';
import {
  DataTable, DataTableHead, DataTableHeaderCell, DataTableBody, DataTableRow, DataTableCell,
} from '@/components/ui/data-table';
import { useFunctionalAreaStatus } from '@/lib/capabilities';

// The complete deployment catalog, including services with no browser endpoint.
// The component test pins this list to the Helm catalog.
export const FUNCTIONAL_AREAS = [
  'user-management', 'device-management', 'event-sources', 'event-management',
  'device-state', 'dashboard-management', 'command-delivery', 'notification-management',
  'event-processing', 'outbound-connectors', 'mcp', 'ai-inference',
  'sparkplug-ingest', 'lwm2m-ingest', 'update-management',
] as const;

export function FunctionalAreasPanel() {
  const { t } = useTranslation('adminSettings');
  return (
    <section className="space-y-4" aria-label={t('functionalAreasTitle')}>
      <HintText>{t('functionalAreasDescription')}</HintText>
      <HintText>{t('functionalAreasEnableHelp')}</HintText>
      <div className="overflow-x-auto">
        <DataTable>
          <DataTableHead>
            <DataTableHeaderCell>{t('functionalAreasArea')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('functionalAreasStatus')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('functionalAreasProvides')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('functionalAreasEnable')}</DataTableHeaderCell>
          </DataTableHead>
          <DataTableBody>
            {FUNCTIONAL_AREAS.map((area) => <AreaRow key={area} area={area} />)}
          </DataTableBody>
        </DataTable>
      </div>
    </section>
  );
}

function AreaRow({ area }: { area: (typeof FUNCTIONAL_AREAS)[number] }) {
  const { t } = useTranslation('adminSettings');
  const status = useFunctionalAreaStatus(area);
  return (
    <DataTableRow>
      <DataTableCell>
        <div className="font-medium">{t(`functionalAreaName_${area}`)}</div>
        <code className="text-xs text-muted-foreground">{area}</code>
      </DataTableCell>
      <DataTableCell>
        <Badge variant={status === 'available' ? 'success' : status === 'absent' ? 'outline' : 'warning'}>
          {t(`functionalAreas${status[0].toUpperCase()}${status.slice(1)}`)}
        </Badge>
      </DataTableCell>
      <DataTableCell>{t(`functionalAreaDescription_${area}`)}</DataTableCell>
      <DataTableCell>
        {status === 'absent'
          ? <code className="break-words text-xs">{`dcctl enable-area <provider> <instance> ${area}`}</code>
          : t(status === 'available' ? 'functionalAreasAlreadyEnabled' : 'functionalAreasUnknownHelp')}
      </DataTableCell>
    </DataTableRow>
  );
}

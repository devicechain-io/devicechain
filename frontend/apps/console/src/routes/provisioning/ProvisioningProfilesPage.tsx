// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The provisioning profiles list. A profile is the shared key and secret a fleet presents to
// register itself, plus the rules for which devices it may register. This page lists them and
// hosts the create drawer; creating one shows the server-generated secret exactly once.

import '@/routes/trust-anchors/catalog';
import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Plus } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@/lib/hooks/use-query';
import { listProvisioningProfiles } from '@/lib/api/provisioning';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { Pagination } from '@/components/ui/pagination';
import { useToast } from '@/components/ui/toast';
import { useAuth } from '@/auth/AuthProvider';
import { rowLinkProps, useReload } from '@/routes/common';
import { mayWrite } from '@/lib/scope';
import { FormDrawer } from '@/components/registry';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import { ProvisioningProfileForm } from './ProvisioningProfileForm';
import { ProfileBadges } from './ProfileBadges';
import { SecretReveal, type RevealedSecret } from './SecretReveal';

const pageSize = 20;

export default function ProvisioningProfilesPage() {
  const { t } = useTranslation('provisioning');
  const navigate = useNavigate();
  const { toast } = useToast();
  const { claims } = useAuth();
  const canWrite = mayWrite(claims, 'device:write');
  const [pageNumber, setPageNumber] = useState(1);
  const [creating, setCreating] = useState(false);
  // The one-time secret, held only while its dialog is open. Component state, so leaving the
  // page drops it too; nothing else reads it and nothing persists it.
  const [revealed, setRevealed] = useState<RevealedSecret | null>(null);
  const [version, reload] = useReload();
  const { data, loading, error } = useQuery(
    () => listProvisioningProfiles({ pageNumber, pageSize }),
    [pageNumber, version],
  );
  const results = data?.results ?? [];

  return (
    <PageShell
      title={t('title')}
      description={t('listDescription')}
      banner="devices"
      action={
        canWrite ? (
          <Button onClick={() => setCreating(true)} data-shot="new-button">
            <Plus size={16} /> {t('newProfile')}
          </Button>
        ) : undefined
      }
    >
      <FormDrawer shot="provisioning-profile-new" open={creating} onOpenChange={setCreating} title={t('newProfile')}>
        <ProvisioningProfileForm
          onCreated={({ profile, issuedSecret }) => {
            toast(t('createdToast', { token: profile.token }));
            setCreating(false);
            reload();
            // A certificate profile has no secret (issuedSecret is null): there is nothing to reveal.
            if (issuedSecret !== null) {
              setRevealed({
                profileToken: profile.token,
                provisionKey: profile.provisionKey,
                secret: issuedSecret,
                rotated: false,
              });
            }
          }}
        />
      </FormDrawer>
      <SecretReveal revealed={revealed} onClose={() => setRevealed(null)} />
      {loading ? (
        <LoadingState description={t('loading')} />
      ) : error ? (
        <ErrorState description={error} />
      ) : results.length === 0 ? (
        <EmptyState description={t('listEmpty')} />
      ) : (
        <>
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('common:colName')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colToken')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colType')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colStrategy')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colRegistered')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colStatus')}</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {results.map((p) => (
                <DataTableRow
                  key={p.id}
                  {...rowLinkProps(() => navigate(`/provisioning-profiles/${encodeURIComponent(p.token)}`))}
                >
                  <DataTableCell className="font-medium text-foreground">{p.name || p.token}</DataTableCell>
                  <DataTableCell>
                    <span className="font-mono text-xs text-foreground">{p.token}</span>
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">{p.deviceType.name || p.deviceType.token}</DataTableCell>
                  <DataTableCell className="text-muted-foreground">{t(`strategy_${p.strategy}`, { defaultValue: p.strategy })}</DataTableCell>
                  <DataTableCell className="tabular-nums text-muted-foreground">
                    {p.maxDevices == null
                      ? t('registeredUncapped', { count: p.provisionedDeviceCount })
                      : t('registeredOfCap', { count: p.provisionedDeviceCount, max: p.maxDevices })}
                  </DataTableCell>
                  <DataTableCell>
                    <ProfileBadges profile={p} />
                  </DataTableCell>
                </DataTableRow>
              ))}
            </DataTableBody>
          </DataTable>
          <Pagination
            pageNumber={pageNumber}
            pageSize={pageSize}
            pagination={data!.pagination}
            onPageChange={setPageNumber}
            className="mt-4"
          />
        </>
      )}
    </PageShell>
  );
}

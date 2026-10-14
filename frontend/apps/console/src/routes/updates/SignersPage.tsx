// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The signer registry: the public keys this tenant trusts to vouch for update artifacts.
//
// Reading needs artifact:read; registering and revoking need artifact:trust, which is NOT
// implied by artifact:write (whoever can upload an image must not be able to appoint the
// signer that vouches for it). A reader without trust sees the list and no actions.
//
// 🔴 This is a ROUTED page, so it branches on status === 'absent' (see UpdateAccess.status),
// never on !present: on a cold load every area is 'unknown' and a !present branch would
// flash "not deployed" on instances that do run the area.
//
// REVOKE states its blast radius from the row it was clicked on: soleSignedArtifactCount
// artifacts have no other ACTIVE signer and become UNVERIFIED. Assignments already sent
// are unaffected — they keep the signature pinned when they were made.

import { useState } from 'react';
import { Plus } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@/lib/hooks/use-query';
import { listArtifactSigners, revokeArtifactSigner, type ArtifactSigner } from '@/lib/api/update-management';
import { useUpdateAccess } from '@/lib/updateAuthority';
import { AREA } from '@/lib/capabilities';
import { AreaUnavailable } from '@/components/AreaUnavailable';
import { PageShell } from '@/components/ui/page-shell';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { ErrorBanner } from '@/components/ui/error-banner';
import { EmptyState } from '@/components/ui/empty-state';
import { Pagination } from '@/components/ui/pagination';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useToast } from '@/components/ui/toast';
import { FormDrawer } from '@/components/registry';
import { formatTime } from '@/lib/utils';
import { errMessage } from '@/routes/common';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import { RegisterSignerForm, SIGNING_GUIDE_URL } from './RegisterSignerForm';

const pageSize = 20;

export default function SignersPage() {
  const { status, canTrust } = useUpdateAccess();
  // The hooks live in SignersList so an absent area never fires the query.
  if (status === 'absent') return <AreaUnavailable area={AREA.updateManagement} />;
  return <SignersList canTrust={canTrust} />;
}

function SignersList({ canTrust }: { canTrust: boolean }) {
  const { t } = useTranslation('updateSigners');
  const confirm = useConfirm();
  const { toast } = useToast();
  const [pageNumber, setPageNumber] = useState(1);
  const [registering, setRegistering] = useState(false);
  const [reload, setReload] = useState(0);
  const [revokeError, setRevokeError] = useState<string | null>(null);

  const { data, loading, error } = useQuery(
    () => listArtifactSigners({ pageNumber, pageSize }),
    [pageNumber, reload],
  );
  const results = data?.results ?? [];

  async function revoke(s: ArtifactSigner) {
    setRevokeError(null);
    const ok = await confirm({
      title: t('revokeTitle', { name: s.name }),
      description: (
        <>
          <span className="block">{t('revokeUnverified', { count: s.soleSignedArtifactCount })}</span>
          <span className="mt-2 block">{t('revokePinned')}</span>
        </>
      ),
      confirmLabel: t('revokeConfirm'),
    });
    if (!ok) return;
    try {
      await revokeArtifactSigner(s.token);
      toast(t('revoked', { name: s.name }));
      setReload((n) => n + 1);
    } catch (err) {
      setRevokeError(errMessage(err));
    }
  }

  return (
    <PageShell
      title={t('title')}
      description={
        <>
          {t('description')}{' '}
          <a href={SIGNING_GUIDE_URL} target="_blank" rel="noopener noreferrer" className="text-primary underline">
            {t('guideLink')}
          </a>
        </>
      }
      action={
        canTrust ? (
          <Button onClick={() => setRegistering(true)} data-shot="new-button">
            <Plus size={16} /> {t('registerSigner')}
          </Button>
        ) : undefined
      }
    >
      {canTrust && (
        <FormDrawer
          shot="signer-register"
          open={registering}
          onOpenChange={setRegistering}
          title={t('registerSigner')}
          description={t('registerDescription')}
        >
          <RegisterSignerForm
            onRegistered={(s) => {
              toast(t('registered', { name: s.name }));
              setRegistering(false);
              setReload((n) => n + 1);
            }}
          />
        </FormDrawer>
      )}
      {revokeError && <ErrorBanner message={revokeError} onDismiss={() => setRevokeError(null)} />}
      {loading && !data ? (
        <LoadingState description={t('loading')} />
      ) : error && !data ? (
        <ErrorState description={error} />
      ) : results.length === 0 ? (
        <EmptyState description={t('empty')} />
      ) : (
        <>
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('colName')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colKeyId')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colAlgorithm')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colState')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colRegistered')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colRevoked')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colSoleSigned')}</DataTableHeaderCell>
              {canTrust && <DataTableHeaderCell>{t('colActions')}</DataTableHeaderCell>}
            </DataTableHead>
            <DataTableBody>
              {results.map((s) => (
                <DataTableRow key={s.id}>
                  <DataTableCell className="font-medium text-foreground">{s.name}</DataTableCell>
                  <DataTableCell className="max-w-48 font-mono text-xs">
                    <span className="block truncate" title={s.keyId}>
                      {s.keyId}
                    </span>
                  </DataTableCell>
                  <DataTableCell className="whitespace-nowrap text-muted-foreground">{s.algorithm}</DataTableCell>
                  <DataTableCell>
                    <Badge variant={s.state === 'ACTIVE' ? 'success' : 'secondary'}>
                      {s.state === 'ACTIVE' ? t('stateActive') : t('stateRevoked')}
                    </Badge>
                  </DataTableCell>
                  <DataTableCell className="whitespace-nowrap text-muted-foreground">
                    {formatTime(s.createdAt)}
                  </DataTableCell>
                  <DataTableCell className="whitespace-nowrap text-muted-foreground">
                    {formatTime(s.revokedAt)}
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">{s.soleSignedArtifactCount}</DataTableCell>
                  {canTrust && (
                    <DataTableCell>
                      {s.state === 'ACTIVE' && (
                        <Button variant="outline" size="sm" onClick={() => void revoke(s)} data-shot="signer-revoke">
                          {t('revoke')}
                        </Button>
                      )}
                    </DataTableCell>
                  )}
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

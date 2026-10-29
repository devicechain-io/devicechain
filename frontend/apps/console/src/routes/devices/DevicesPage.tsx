// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useMemo, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { Plus, Layers, Upload, X } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { cn } from '@/lib/utils';
import { useQuery } from '@/lib/hooks/use-query';
import { listDevices, type DeviceSearchResults } from '@/lib/api/device-management';
import { isoTime, type CsvColumn } from '@/lib/csv';
import { ExportCsvButton } from '@/components/ExportCsvButton';
import { getDeviceStates } from '@/lib/api/device-state';
import { presenceKind, type PresenceFacts } from '@/lib/presence';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { TypeCapsule, appearanceOf } from '@/components/TypeCapsule';
import { Pagination } from '@/components/ui/pagination';
import { useToast } from '@/components/ui/toast';
import { rowLinkProps, useReload } from '@/routes/common';
import { useAuth } from '@/auth/AuthProvider';
import { hasAuthority, isScoped } from '@devicechain/client';
import { getDeviceEarmarks, listProvisioningProfiles, type ProfileRef } from '@/lib/api/provisioning';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { FormDrawer } from '@/components/registry';
import { DeviceForm } from '@/routes/devices/DeviceForm';
import { NEW_ALIAS_PARAM, NEW_TYPE_PARAM, rowFromParam } from '@/routes/devices/deviceAliases';
import { DeviceBulkForm } from '@/routes/devices/DeviceBulkForm';
import { DeviceImportForm } from '@/routes/devices/DeviceImportForm';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';

const pageSize = 20;
// The URL parameters (and criteria fields) the provisioning filters ride on.
const REGISTERED_PARAM = 'provisioningProfile';
const EXPECTED_PARAM = 'expectedProvisioningProfile';
const EXPORT_STEM = 'devices';
// The import maps columns and lists per-row outcomes, so its drawer is wider than a form's.
const IMPORT_DRAWER_WIDTH = 'sm:max-w-2xl';

type DeviceRow = DeviceSearchResults['results'][number];

// StatusDot shows a device's connectivity at a glance. It is handed the whole state
// row rather than its `active` flag, because the flag alone cannot tell a REPORTED
// disconnect from silence and the column now says which one it is. `undefined` means
// no state is known (no state:read authority, or the device has never reported) —
// rendered as a neutral dash so the list stays useful without it.
//
// 🔴 The inferred rendering is byte-identical to what shipped before the split: same
// dot, same muted text, same "Offline". Only the ASSERTED case gains new wording —
// nothing an operator already recognises moved, and the dot is deliberately NOT split
// as well, because a colour is not where a distinction this specific belongs. The
// detail view is where the difference is spelled out.
function StatusDot({ state }: { state: PresenceFacts | undefined }) {
  const { t } = useTranslation('devices');
  const kind = presenceKind(state);
  if (kind === 'unknown') return <span className="text-muted-foreground">—</span>;
  const online = kind === 'online';
  return (
    <span className="inline-flex items-center gap-1.5 text-sm" data-shot-mask>
      <span className={cn('inline-block size-2 rounded-full', online ? 'bg-success' : 'bg-muted-foreground/40')} />
      <span className={online ? 'text-foreground' : 'text-muted-foreground'}>
        {online ? t('online') : kind === 'disconnected' ? t('disconnected') : t('offline')}
      </span>
    </span>
  );
}

export default function DevicesPage() {
  const { t } = useTranslation('devices');
  const navigate = useNavigate();
  const { toast } = useToast();
  const [pageNumber, setPageNumber] = useState(1);
  const [params, setParams] = useSearchParams();
  // An integration's unregistered list links here with the alias it saw (and the type it can
  // resolve), so the drawer opens ready to create that device.
  const aliasSeed = useMemo(() => rowFromParam(params.get(NEW_ALIAS_PARAM)), [params]);
  const [creating, setCreating] = useState(() => aliasSeed != null);
  const setCreateOpen = (open: boolean) => {
    setCreating(open);
    if (!open && params.has(NEW_ALIAS_PARAM)) {
      const next = new URLSearchParams(params);
      next.delete(NEW_ALIAS_PARAM);
      next.delete(NEW_TYPE_PARAM);
      setParams(next, { replace: true });
    }
  };
  const [bulkCreating, setBulkCreating] = useState(false);
  const [importing, setImporting] = useState(false);
  const [version, reload] = useReload();
  // Self-registration provenance is for members who may see the whole tenant: the server
  // refuses the profile fields to one limited to part of it, so a scoped member's page never
  // names them (no filter, no column, no extra request).
  const { claims } = useAuth();
  const scoped = isScoped(claims);
  // Creating, importing and bulk-creating are device:write, which the server refuses to a
  // member limited to part of the tenant whatever their role names.
  const canWrite = !scoped && hasAuthority(claims, 'device:write');
  const provisioningProfile = scoped ? '' : (params.get(REGISTERED_PARAM) ?? '');
  const expectedProvisioningProfile = scoped ? '' : (params.get(EXPECTED_PARAM) ?? '');
  const filtered = provisioningProfile !== '' || expectedProvisioningProfile !== '';
  const setFilter = (key: typeof REGISTERED_PARAM | typeof EXPECTED_PARAM, value: string) => {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    setParams(next, { replace: true });
    setPageNumber(1);
  };
  const { data, loading, error } = useQuery(
    () =>
      listDevices({
        pageNumber,
        pageSize,
        provisioningProfile: provisioningProfile || undefined,
        expectedProvisioningProfile: expectedProvisioningProfile || undefined,
      }),
    [pageNumber, version, provisioningProfile, expectedProvisioningProfile],
  );
  const { data: profileList } = useQuery(
    () => (scoped ? Promise.resolve(null) : listProvisioningProfiles({ pageNumber: 1, pageSize: 1000 })),
    [scoped],
  );
  const profileOptions: ComboboxOption[] = (profileList?.results ?? []).map((p) => ({
    value: p.token,
    label: p.name || p.token,
    description: p.name ? p.token : undefined,
  }));

  // The export mirrors the list's columns (status is a live projection, so it is left out)
  // plus the type's token; timestamps are ISO-8601.
  const exportColumns: CsvColumn<DeviceRow>[] = [
    { header: t('common:colToken'), value: (d) => d.token },
    { header: t('common:colName'), value: (d) => d.name },
    { header: t('common:colType'), value: (d) => d.deviceType?.name },
    { header: t('exportColTypeToken'), value: (d) => d.deviceType?.token },
    { header: t('common:colDescription'), value: (d) => d.description },
    { header: t('common:colCreated'), value: (d) => isoTime(d.createdAt) },
  ];

  const results = data?.results ?? [];
  const tokens = results.map((d) => d.token);
  // Status is best-effort and loaded separately: if state:read is missing or no
  // state exists yet, this query just yields nothing and the list is unaffected.
  const { data: states } = useQuery(() => getDeviceStates(tokens), [tokens.join(',')]);
  const stateByToken = new Map((states ?? []).map((s) => [s.deviceToken, s]));
  // The profile each listed device is earmarked for, loaded separately for the same reason.
  const { data: earmarks } = useQuery(
    () => (scoped ? Promise.resolve<Record<string, ProfileRef>>({}) : getDeviceEarmarks(tokens)),
    [tokens.join(','), scoped],
  );
  const profileLabel = (token: string) => {
    const p = profileList?.results.find((r) => r.token === token);
    return p?.name || token;
  };

  return (
    <PageShell
      title={t('title')}
      description={t('description')}
      banner="devices"
      action={
        <div className="flex gap-2">
          <ExportCsvButton
            fileStem={EXPORT_STEM}
            columns={exportColumns}
            fetchPage={(pageNumber, pageSize) => listDevices({ pageNumber, pageSize })}
          />
          {canWrite && (
            <>
              <Button variant="outline" onClick={() => setImporting(true)} data-shot="device-import-button">
                <Upload size={16} /> {t('importDevices')}
              </Button>
              <Button variant="outline" onClick={() => setBulkCreating(true)} data-shot="device-bulk-button">
                <Layers size={16} /> {t('bulkCreate')}
              </Button>
              <Button onClick={() => setCreating(true)} data-shot="device-new-button">
                <Plus size={16} /> {t('newDevice')}
              </Button>
            </>
          )}
        </div>
      }
    >
      <FormDrawer
        shot="device-new"
        open={creating}
        onOpenChange={setCreateOpen}
        title={t('newDevice')}
      >
        <DeviceForm
          offerEarmark={!scoped}
          offerAliases={!scoped}
          initialAliases={aliasSeed ? [aliasSeed] : undefined}
          initialTypeToken={params.get(NEW_TYPE_PARAM) ?? undefined}
          onDone={(m) => {
            toast(m);
            setCreateOpen(false);
            reload();
          }}
        />
      </FormDrawer>
      <FormDrawer shot="device-bulk" open={bulkCreating} onOpenChange={setBulkCreating} title={t('bulkCreateTitle')}>
        <DeviceBulkForm
          onDone={(m) => {
            toast(m);
            setBulkCreating(false);
            reload();
          }}
        />
      </FormDrawer>
      <FormDrawer
        shot="device-import"
        open={importing}
        onOpenChange={setImporting}
        title={t('importTitle')}
        contentClassName={IMPORT_DRAWER_WIDTH}
      >
        <DeviceImportForm
          onDone={(m) => {
            toast(m);
            setImporting(false);
            reload();
          }}
        />
      </FormDrawer>
      {!scoped && profileOptions.length > 0 && (
        <div className="mb-4 flex flex-wrap items-end gap-4" data-shot="device-provisioning-filters">
          <div className="w-64">
            <label htmlFor="dev-filter-registered" className="mb-1 block text-xs font-medium text-muted-foreground">
              {t('filterRegisteredBy')}
            </label>
            <Combobox
              id="dev-filter-registered"
              value={provisioningProfile}
              onChange={(v) => setFilter(REGISTERED_PARAM, v)}
              options={profileOptions}
              placeholder={t('filterAnyProfile')}
            />
          </div>
          <div className="w-64">
            <label htmlFor="dev-filter-expected" className="mb-1 block text-xs font-medium text-muted-foreground">
              {t('filterExpectedProfile')}
            </label>
            <Combobox
              id="dev-filter-expected"
              value={expectedProvisioningProfile}
              onChange={(v) => setFilter(EXPECTED_PARAM, v)}
              options={profileOptions}
              placeholder={t('filterAnyProfile')}
            />
          </div>
          {filtered && (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                setParams(new URLSearchParams(), { replace: true });
                setPageNumber(1);
              }}
            >
              <X size={14} /> {t('filterClear')}
            </Button>
          )}
        </div>
      )}
      {loading ? (
        <LoadingState description={t('loading')} />
      ) : error ? (
        <ErrorState description={error} />
      ) : results.length === 0 ? (
        <EmptyState description={filtered ? t('emptyFiltered') : t('empty')} />
      ) : (
        <>
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('common:colStatus')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colToken')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colName')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colType')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colDescription')}</DataTableHeaderCell>
              {!scoped && <DataTableHeaderCell>{t('colProvisioning')}</DataTableHeaderCell>}
              <DataTableHeaderCell>{t('common:colCreated')}</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {results.map((device) => (
                <DataTableRow
                  key={device.id}
                  {...rowLinkProps(() => navigate(`/devices/${encodeURIComponent(device.token)}`))}
                >
                  <DataTableCell>
                    <StatusDot state={stateByToken.get(device.token)} />
                  </DataTableCell>
                  <DataTableCell>
                    <span className="font-mono text-xs text-foreground">{device.token}</span>
                  </DataTableCell>
                  <DataTableCell className="font-medium text-foreground">
                    <span className="inline-flex items-center gap-2">
                      {device.name || '—'}
                      {!device.enabled && <Badge variant="warning">{t('deviceDisabledBadge')}</Badge>}
                    </span>
                  </DataTableCell>
                  <DataTableCell>
                    <TypeCapsule appearance={appearanceOf(device.deviceType)} />
                  </DataTableCell>
                  <DataTableCell className="max-w-xs truncate text-muted-foreground">
                    {device.description || '—'}
                  </DataTableCell>
                  {!scoped && (
                    <DataTableCell data-shot-mask>
                      <ProvisioningCell
                        registeredBy={provisioningProfile ? profileLabel(provisioningProfile) : null}
                        expected={
                          earmarks?.[device.token]
                            ? earmarks[device.token].name || earmarks[device.token].token
                            : null
                        }
                      />
                    </DataTableCell>
                  )}
                  <DataTableCell className="text-muted-foreground">
                    {device.createdAt ? new Date(device.createdAt).toLocaleDateString() : '—'}
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

// What the list can say about a device's self-registration without a query per row. The
// earmark is loaded for the whole page; the profile that REGISTERED the device is known only
// when the list is filtered by it (every row then holds a credential that profile issued), so
// the unfiltered list does not claim a provenance it has not read. The device's page has both.
function ProvisioningCell({ registeredBy, expected }: { registeredBy: string | null; expected: string | null }) {
  const { t } = useTranslation('devices');
  if (!registeredBy && !expected) return <span className="text-muted-foreground">—</span>;
  return (
    <span className="flex flex-col gap-0.5 text-xs">
      {registeredBy && <span className="text-foreground">{t('provisionedBy', { profile: registeredBy })}</span>}
      {expected && <span className="text-muted-foreground">{t('expectedProfile', { profile: expected })}</span>}
    </span>
  );
}

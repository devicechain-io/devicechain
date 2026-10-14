// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The device-type create/edit form: token, name, description and the maintenance window
// that holds the type's disruptive commands. It is the registry type form plus one picker.
//
// An edit is a PARTIAL update. It always names name and description (the fields the form
// has always edited), and names the maintenance window ONLY when the operator changed it —
// `undefined` keeps, `null` detaches (see windowArg.ts).

import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { ArrowRight } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { FormField } from '@/components/ui/form-field';
import { TokenField } from '@/components/ui/token-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage } from '@/routes/common';
import { createDeviceType, updateDeviceType, type DeviceType } from '@/lib/api/device-management';
import { listMaintenanceWindows } from '@/lib/api/maintenance-windows';
import { maintenanceWindowArg } from './windowArg';

export function DeviceTypeForm({
  entity,
  onDone,
}: {
  entity?: DeviceType;
  onDone: (message: string) => void;
}) {
  const { t } = useTranslation(['entities', 'common']);
  const editing = entity != null;
  const initialWindow = entity?.maintenanceWindow?.token ?? '';
  const [token, setToken] = useState(entity?.token ?? '');
  const [name, setName] = useState(entity?.name ?? '');
  const [description, setDescription] = useState(entity?.description ?? '');
  const [windowToken, setWindowToken] = useState(initialWindow);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const { data, loading } = useQuery(() => listMaintenanceWindows({ pageNumber: 1, pageSize: 1000 }), []);
  const options: ComboboxOption[] = (data?.results ?? []).map((w) => ({
    value: w.token,
    label: w.name ? `${w.name} (${w.token})` : w.token,
    description: w.timezone,
  }));
  // The assigned window stays selectable-by-name even if the list did not return it.
  if (initialWindow && !options.some((o) => o.value === initialWindow) && entity?.maintenanceWindow) {
    const w = entity.maintenanceWindow;
    options.push({
      value: w.token,
      label: w.name ? `${w.name} (${w.token})` : w.token,
      description: w.timezone,
    });
  }

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      const fields = { name: name.trim() || null, description: description.trim() || null };
      const windowArg = maintenanceWindowArg(initialWindow, windowToken, editing);
      if (editing) {
        await updateDeviceType(entity.token, {
          ...fields,
          ...(windowArg !== undefined && { maintenanceWindowToken: windowArg }),
        });
        onDone(t('entities:deviceTypeUpdatedToast', { token: entity.token }));
      } else {
        const trimmed = token.trim();
        await createDeviceType({
          token: trimmed,
          ...fields,
          ...(windowArg !== undefined && { maintenanceWindowToken: windowArg }),
        });
        onDone(t('entities:deviceTypeCreatedToast', { token: trimmed }));
      }
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      <FormField
        label={t('common:colToken')}
        htmlFor="r-token"
        description={editing ? t('entities:deviceTypeTokenFixed') : undefined}
      >
        {editing ? (
          <Input id="r-token" value={token} disabled />
        ) : (
          <TokenField
            id="r-token"
            entityType="device-type"
            value={token}
            onChange={setToken}
            seed={name}
            placeholder={t('entities:deviceTypeTokenPlaceholder')}
          />
        )}
      </FormField>
      <FormField label={t('common:colName')} htmlFor="r-name">
        <Input id="r-name" value={name} onChange={(ev) => setName(ev.target.value)} />
      </FormField>
      <FormField label={t('common:colDescription')} htmlFor="r-description">
        <Textarea id="r-description" value={description} onChange={(ev) => setDescription(ev.target.value)} />
      </FormField>
      <FormField
        label={t('entities:deviceTypeMaintenanceWindow')}
        htmlFor="dt-window"
        description={t('entities:deviceTypeMaintenanceWindowHelp')}
      >
        <Combobox
          id="dt-window"
          options={options}
          value={windowToken}
          onChange={setWindowToken}
          allowClear
          placeholder={
            loading
              ? t('entities:deviceTypeMaintenanceWindowLoading')
              : t('entities:deviceTypeMaintenanceWindowPlaceholder')
          }
          emptyMessage={t('entities:deviceTypeMaintenanceWindowEmpty')}
        />
        {windowToken && (
          <Link
            to={`/maintenance-windows/${encodeURIComponent(windowToken)}`}
            className="mt-1 inline-flex items-center gap-1 text-sm font-medium text-primary hover:underline"
          >
            {t('entities:deviceTypeMaintenanceWindowOpen')} <ArrowRight size={14} />
          </Link>
        )}
      </FormField>
      <div className="flex gap-2">
        <Button onClick={submit} loading={busy} disabled={busy || (!editing && !token.trim())}>
          {editing ? t('common:saveChanges') : t('entities:deviceTypeCreateAction')}
        </Button>
      </div>
    </div>
  );
}

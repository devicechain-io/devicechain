// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The "Identity" tab of a device-type detail (ADR-045 decision 8). Edits the
// manufacturer and model discovery facets — the "what device this type is" that
// stays correct even when many types share one capability profile. Both are
// free-text with suggestion lists drawn from the values already in use, so a
// tenant's fleet vocabulary stays consistent. DeviceType update is full-replace, so
// this carries everything else forward via deviceTypePreserved.

import { useState } from 'react';
import { Button } from '@/components/ui/button';
import { FormField } from '@/components/ui/form-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { SuggestField } from '@/components/ui/suggest-field';
import { useToast } from '@/components/ui/toast';
import { errMessage } from '@/routes/common';
import { useAuth } from '@/auth/AuthProvider';
import { hasAuthority } from '@devicechain/client';
import { updateDeviceType, deviceTypePreserved, type DeviceType } from '@/lib/api/device-management';

export function TypeIdentityForm({ entity, onSaved }: { entity: DeviceType; onSaved: () => void }) {
  const { claims } = useAuth();
  const canWrite = hasAuthority(claims, 'device:write');
  const { toast } = useToast();
  const [manufacturer, setManufacturer] = useState(entity.manufacturer ?? '');
  const [model, setModel] = useState(entity.model ?? '');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Compare trimmed values — submit sends trimmed, so a whitespace-only edit is not
  // "dirty" and Save stays disabled (it would otherwise re-enable after a no-op save,
  // since the tab does not remount on the parent reload).
  const dirty =
    manufacturer.trim() !== (entity.manufacturer ?? '') || model.trim() !== (entity.model ?? '');

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      await updateDeviceType(entity.token, {
        ...deviceTypePreserved(entity),
        manufacturer: manufacturer.trim() || null,
        model: model.trim() || null,
      });
      toast('Identity saved');
      onSaved();
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="max-w-xl space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      <p className="max-w-prose text-sm text-muted-foreground">
        Manufacturer and model identify the physical device. They are discovery facets — used to filter
        and group — so the fields suggest values already in use to keep them consistent, while still
        letting you type a new one.
      </p>
      <FormField label="Manufacturer" htmlFor="dt-manufacturer">
        <SuggestField
          id="dt-manufacturer"
          facet="MANUFACTURER"
          value={manufacturer}
          onChange={setManufacturer}
          placeholder="Acme Corp"
          disabled={!canWrite}
        />
      </FormField>
      <FormField label="Model" htmlFor="dt-model">
        <SuggestField id="dt-model" facet="MODEL" value={model} onChange={setModel} placeholder="TS-100" disabled={!canWrite} />
      </FormField>
      {canWrite && (
        <div className="flex gap-2">
          <Button onClick={submit} loading={busy} disabled={busy || !dirty}>
            Save identity
          </Button>
        </div>
      )}
    </div>
  );
}

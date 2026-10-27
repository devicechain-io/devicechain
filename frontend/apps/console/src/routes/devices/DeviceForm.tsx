// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { RegistryInstanceForm } from '@/components/registry';
import { ProvisioningProfilePicker } from '@/routes/provisioning/ProvisioningProfilePicker';
import { DeviceAliasesField } from './DeviceAliasesField';
import { aliasInputs, aliasesChanged, aliasesValid, rowsOf, type AliasRow } from './deviceAliases';
import {
  listDeviceTypes,
  createDevice,
  updateDevice,
  getDevice,
  type Device,
} from '@/lib/api/device-management';

// The device create/edit form — the generic instance form (token/name/description
// + required type picker) wired to the device APIs. Used by the create drawer on
// the devices list and the device detail page. Its noun-bearing prose resolves
// from the `entities` catalog under the `device` family prefix.
export function DeviceForm({
  device,
  onDone,
  readOnly,
  offerEarmark = false,
  offerAliases = false,
  initialAliases,
  initialTypeToken,
}: {
  device?: Device & { aliases?: readonly { scheme: string; value: string }[] | null };
  onDone: (message: string) => void;
  /** Show the device without offering to change it (a member without device:write). */
  readOnly?: boolean;
  /**
   * Offer the "expected provisioning profile" field on create: the one profile that may later
   * adopt the device when it registers itself. Off by default, and the caller leaves it off for
   * a member limited to part of the tenant, because the server refuses them the profile list.
   */
  offerEarmark?: boolean;
  /**
   * Offer the alias field: the names a sender other than the platform knows the device by. On
   * edit it also needs the device's aliases to have been read (they are null for a member
   * limited to part of the tenant, who is never offered the field).
   */
  offerAliases?: boolean;
  /** Create only: aliases to start with, such as one an integration saw and no device carries. */
  initialAliases?: AliasRow[];
  /** Create only: the type to start with. */
  initialTypeToken?: string;
}) {
  const [earmark, setEarmark] = useState('');
  const editing = device != null;
  // The rows the form was seeded from; an edit sends the list only when it differs from them.
  const [seed] = useState<AliasRow[]>(() => (editing ? rowsOf(device.aliases) : (initialAliases ?? [])));
  const [aliases, setAliases] = useState<AliasRow[]>(seed);
  const showAliases = offerAliases && (!editing || device.aliases != null);
  return (
    <RegistryInstanceForm
      entity={device}
      i18nKey="device"
      entityType="device"
      checkAvailability={(token) => getDevice(token).then((d) => d === null)}
      defaultTypeToken={device?.deviceType.token ?? initialTypeToken}
      loadTypes={() => listDeviceTypes({ pageNumber: 1, pageSize: 1000 }).then((r) => r.results)}
      create={(req) =>
        createDevice({
          token: req.token,
          name: req.name,
          description: req.description,
          deviceTypeToken: req.typeToken,
          // Absent, not null, when none is chosen: the field is for callers who can use it.
          ...(earmark ? { provisioningProfileToken: earmark } : {}),
          ...(showAliases && aliasInputs(aliases).length > 0 ? { aliases: aliasInputs(aliases) } : {}),
        })
      }
      renderCreateExtra={
        offerEarmark
          ? ({ typeToken, disabled }) => (
              <ProvisioningProfilePicker
                value={earmark}
                onChange={setEarmark}
                deviceTypeToken={typeToken}
                disabled={disabled}
              />
            )
          : undefined
      }
      update={(token, req) =>
        // A partial update: this form edits the name, the description and the type,
        // so it sends exactly those. externalId — the handle whatever system
        // provisioned the device correlates it by — and metadata survive by being
        // ABSENT. Under the full replace this replaces, renaming a device here
        // deleted both, with a success toast either way.
        updateDevice(token, {
          name: req.name,
          description: req.description,
          deviceTypeToken: req.typeToken,
          // The list REPLACES the stored one, so it is named only when the operator changed it.
          ...(showAliases && aliasesChanged(seed, aliases) ? { aliases: aliasInputs(aliases) } : {}),
        })
      }
      renderExtra={
        showAliases
          ? ({ disabled }) => <DeviceAliasesField rows={aliases} onChange={setAliases} disabled={disabled} />
          : undefined
      }
      extraInvalid={showAliases && !aliasesValid(aliases)}
      onDone={onDone}
      readOnly={readOnly}
    />
  );
}

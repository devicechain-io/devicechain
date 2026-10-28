// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/routes/trust-anchors/catalog';
import { useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { FormField } from '@/components/ui/form-field';
import { useQuery } from '@/lib/hooks/use-query';
import { listProvisioningProfiles } from '@/lib/api/provisioning';
import { DERIVED_TOKEN_PREFIX } from '@/routes/trust-anchors/anchorRules';

/**
 * The "expected provisioning profile" field of a device: an OPTIONAL pick of the profile that
 * may adopt the device when it registers itself. Only profiles of the device's type are offered,
 * because the server refuses an earmark across types, and a pick the new type no longer admits
 * is cleared rather than left to fail on save.
 *
 * Callers render it for UNSCOPED members only: listing profiles is refused to a member limited
 * to part of the tenant.
 */
export function ProvisioningProfilePicker({
  value,
  onChange,
  deviceTypeToken,
  disabled,
  id = 'dev-earmark',
}: {
  value: string;
  onChange: (token: string) => void;
  /** The device's type token; with none chosen yet, every profile is offered. */
  deviceTypeToken?: string;
  disabled?: boolean;
  id?: string;
}) {
  const { t } = useTranslation('provisioning');
  const { data, loading } = useQuery(
    () => listProvisioningProfiles({ pageNumber: 1, pageSize: 1000, deviceType: deviceTypeToken || undefined }),
    [deviceTypeToken],
  );
  const options: ComboboxOption[] = (data?.results ?? []).map((p) => ({
    value: p.token,
    label: p.name || p.token,
    description: p.name ? p.token : undefined,
  }));

  const selectedIsCertificate = (data?.results ?? []).some((p) => p.token === value && p.trustAnchor != null);

  // A pick the (new) type does not admit would be refused by the server: drop it once the
  // list for the current type has actually loaded.
  useEffect(() => {
    if (!loading && data && value && !options.some((o) => o.value === value)) onChange('');
  });

  return (
    <FormField
      label={t('earmarkLabel')}
      htmlFor={id}
      // A certificate profile adopts the device by its certificate: the token is not free text.
      description={
        selectedIsCertificate ? t('trustAnchors:earmarkDerivedHint', { prefix: DERIVED_TOKEN_PREFIX }) : t('earmarkHelp')
      }
    >
      <Combobox
        id={id}
        value={value}
        onChange={onChange}
        options={options}
        placeholder={t('earmarkNone')}
        emptyMessage={t('earmarkEmpty')}
        disabled={disabled}
      />
    </FormField>
  );
}

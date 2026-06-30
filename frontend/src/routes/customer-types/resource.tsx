// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryTypeForm, tokenColumn, descriptionColumn, createdColumn, type RegistryResource } from '@/components/registry';
import { TypeCapsule, appearanceOf } from '@/components/TypeCapsule';
import { TypeAppearanceForm } from '@/components/TypeAppearanceForm';
import {
  listCustomerTypes,
  getCustomerType,
  createCustomerType,
  updateCustomerType,
  deleteCustomerType,
  type CustomerType,
} from '@/lib/api/customers';

// The customer-type registry, described once for the generic list/detail/new pages.
export const customerTypeResource: RegistryResource<CustomerType> = {
  basePath: '/customer-types',
  titlePlural: 'Customer Types',
  singular: 'customer type',
  banner: 'customers',
  listDescription: 'Templates that classify customers',
  list: listCustomerTypes,
  load: getCustomerType,
  remove: deleteCustomerType,
  idOf: (ct) => ct.id,
  tokenOf: (ct) => ct.token,
  nameOf: (ct) => ct.name,
  columns: [
    {
      header: 'Appearance',
      cell: (ct) => <TypeCapsule appearance={appearanceOf(ct)} />,
    },
    tokenColumn<CustomerType>(),
    descriptionColumn<CustomerType>(),
    createdColumn<CustomerType>(),
  ],
  renderForm: (ct, onDone) => (
    <RegistryTypeForm
      entity={ct}
      singular="customer type"
      tokenPlaceholder="standard"
      create={(req) => createCustomerType(req)}
      update={(token, req) =>
        updateCustomerType(token, {
          token: req.token,
          name: req.name,
          description: req.description,
          icon: ct?.icon,
          backgroundColor: ct?.backgroundColor,
          foregroundColor: ct?.foregroundColor,
          borderColor: ct?.borderColor,
        })
      }
      onDone={onDone}
    />
  ),
  removeConfirm: (ct) => `Delete customer type “${ct.token}”? This cannot be undone.`,
  detailExtraLabel: 'Appearance',
  renderDetailExtra: (ct, reload) => (
    <TypeAppearanceForm
      entity={ct}
      update={(req) => updateCustomerType(ct.token, req)}
      onSaved={reload}
    />
  ),
};

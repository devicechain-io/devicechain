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
  customerTypePreserved,
  deleteCustomerType,
  type CustomerType,
} from '@/lib/api/customers';

// The customer-type registry, described once for the generic list/detail/new pages.
export const customerTypeResource: RegistryResource<CustomerType> = {
  basePath: '/customer-types',
  i18nKey: 'customerType',
  banner: 'customers',
  list: listCustomerTypes,
  load: getCustomerType,
  remove: deleteCustomerType,
  idOf: (ct) => ct.id,
  tokenOf: (ct) => ct.token,
  nameOf: (ct) => ct.name,
  columns: [
    {
      header: 'common:colAppearance',
      cell: (ct) => <TypeCapsule appearance={appearanceOf(ct)} />,
    },
    tokenColumn<CustomerType>(),
    descriptionColumn<CustomerType>(),
    createdColumn<CustomerType>(),
  ],
  renderForm: (ct, onDone) => (
    <RegistryTypeForm
      entity={ct}
      i18nKey="customerType"
      entityType="customer-type"
      create={(req) => createCustomerType(req)}
      update={(token, req) =>
        // RegistryTypeForm calls update only when editing, so ct is set. The
        // appearance fields were already carried by hand here; customerTypePreserved
        // adds the imageUrl and metadata that were not, and makes the next field
        // added to the schema a compile error rather than a silent deletion.
        updateCustomerType(token, {
          ...customerTypePreserved(ct!),
          name: req.name,
          description: req.description,
        })
      }
      onDone={onDone}
    />
  ),
  detailExtraLabel: 'common:colAppearance',
  renderDetailExtra: (ct, reload) => (
    <TypeAppearanceForm
      entity={ct}
      // The appearance tab edits icon + colors; everything else — name,
      // description, imageUrl, metadata — has to be carried, or saving a colour
      // deletes it.
      update={(req) => updateCustomerType(ct.token, { ...customerTypePreserved(ct), ...req })}
      onSaved={reload}
    />
  ),
};

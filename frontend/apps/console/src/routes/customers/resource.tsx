// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryInstanceForm, tokenColumn, nameColumn, createdColumn, type RegistryResource } from '@/components/registry';
import { TypeCapsule, appearanceOf } from '@/components/TypeCapsule';
import {
  listCustomers,
  getCustomer,
  deleteCustomer,
  createCustomer,
  updateCustomer,
  listCustomerTypes,
  type Customer,
} from '@/lib/api/customers';

export const customerResource: RegistryResource<Customer> = {
  basePath: '/customers',
  i18nKey: 'customer',
  banner: 'customers',
  list: listCustomers,
  load: getCustomer,
  remove: deleteCustomer,
  idOf: (c) => c.id,
  tokenOf: (c) => c.token,
  nameOf: (c) => c.name,
  typeOf: (c) => (c.customerType ? appearanceOf(c.customerType) : null),
  columns: [
    tokenColumn<Customer>(),
    nameColumn<Customer>(),
    {
      header: 'common:colType',
      cell: (c) =>
        c.customerType ? (
          <TypeCapsule appearance={appearanceOf(c.customerType)} />
        ) : (
          <span className="text-muted-foreground">—</span>
        ),
    },
    createdColumn<Customer>(),
  ],
  renderForm: (c, onDone) => (
    <RegistryInstanceForm
      entity={c}
      i18nKey="customer"
      entityType="customer"
      defaultTypeToken={c?.customerType?.token}
      loadTypes={() => listCustomerTypes({ pageNumber: 1, pageSize: 1000 }).then((r) => r.results)}
      create={(req) =>
        createCustomer({
          token: req.token,
          name: req.name,
          description: req.description,
          customerTypeToken: req.typeToken,
        })
      }
      update={(token, req) =>
        updateCustomer(token, {
          token: req.token,
          name: req.name,
          description: req.description,
          customerTypeToken: req.typeToken,
        })
      }
      onDone={onDone}
    />
  ),
};

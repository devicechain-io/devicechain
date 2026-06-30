// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryInstanceForm, type RegistryResource } from '@/components/registry';
import { Badge } from '@/components/ui/badge';
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
  titlePlural: 'Customers',
  singular: 'customer',
  backLabel: 'Customers',
  listDescription: 'Organizations or accounts you serve',
  banner: 'customers',
  list: listCustomers,
  load: getCustomer,
  remove: deleteCustomer,
  idOf: (c) => c.id,
  tokenOf: (c) => c.token,
  descriptionOf: (c) => c.name ?? '—',
  columns: [
    {
      header: 'Token',
      cell: (c) => <span className="font-mono text-xs text-foreground">{c.token}</span>,
    },
    { header: 'Name', cell: (c) => c.name || '—', className: 'font-medium text-foreground' },
    {
      header: 'Type',
      cell: (c) =>
        c.customerType ? (
          <Badge variant="secondary">{c.customerType.name || c.customerType.token}</Badge>
        ) : (
          <span className="text-muted-foreground">—</span>
        ),
    },
    {
      header: 'Created',
      cell: (c) => (c.createdAt ? new Date(c.createdAt).toLocaleDateString() : '—'),
      className: 'text-muted-foreground',
    },
  ],
  renderForm: (c, onDone) => (
    <RegistryInstanceForm
      entity={c}
      singular="customer"
      typeLabel="Customer type"
      typeSingular="customer type"
      tokenPlaceholder="acme"
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
  removeConfirm: (c) => `Delete customer “${c.token}”? This cannot be undone.`,
};

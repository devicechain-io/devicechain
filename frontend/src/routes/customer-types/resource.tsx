// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryTypeForm, type RegistryResource } from '@/components/registry';
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
  backLabel: 'Customer types',
  listDescription: 'Templates that classify customers',
  list: listCustomerTypes,
  load: getCustomerType,
  remove: deleteCustomerType,
  idOf: (ct) => ct.id,
  tokenOf: (ct) => ct.token,
  descriptionOf: (ct) => ct.name ?? '—',
  columns: [
    {
      header: 'Token',
      cell: (ct) => (
        <div className="flex items-center gap-2">
          <span
            className={
              ct.backgroundColor ? 'size-4 shrink-0 rounded' : 'size-4 shrink-0 rounded bg-muted'
            }
            style={ct.backgroundColor ? { backgroundColor: ct.backgroundColor } : undefined}
            aria-hidden
          />
          <span className="font-mono text-xs text-foreground">{ct.token}</span>
        </div>
      ),
    },
    { header: 'Name', cell: (ct) => ct.name || '—', className: 'font-medium text-foreground' },
    { header: 'Description', cell: (ct) => ct.description || '—', className: 'text-muted-foreground' },
    {
      header: 'Created',
      cell: (ct) => (ct.createdAt ? new Date(ct.createdAt).toLocaleDateString() : '—'),
      className: 'text-muted-foreground',
    },
  ],
  renderForm: (ct, onDone) => (
    <RegistryTypeForm
      entity={ct}
      singular="customer type"
      tokenPlaceholder="standard"
      create={(req) => createCustomerType(req)}
      update={(token, req) => updateCustomerType(token, req)}
      onDone={onDone}
    />
  ),
  removeConfirm: (ct) => `Delete customer type “${ct.token}”? This cannot be undone.`,
};

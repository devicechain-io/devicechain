// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryTypeForm, type RegistryResource } from '@/components/registry';
import { MembershipPanel } from '@/components/MembershipPanel';
import {
  listCustomerGroups,
  getCustomerGroup,
  createCustomerGroup,
  updateCustomerGroup,
  deleteCustomerGroup,
  listCustomers,
  type CustomerGroup,
} from '@/lib/api/customers';

export const customerGroupResource: RegistryResource<CustomerGroup> = {
  basePath: '/customer-groups',
  titlePlural: 'Customer Groups',
  singular: 'customer group',
  backLabel: 'Customer groups',
  banner: 'customers',
  listDescription: 'Collections of customers',
  list: listCustomerGroups,
  load: getCustomerGroup,
  remove: deleteCustomerGroup,
  idOf: (g) => g.id,
  tokenOf: (g) => g.token,
  descriptionOf: (g) => g.name ?? '—',
  columns: [
    {
      header: 'Token',
      cell: (g) => <span className="font-mono text-xs text-foreground">{g.token}</span>,
    },
    { header: 'Name', cell: (g) => g.name || '—', className: 'font-medium text-foreground' },
    { header: 'Description', cell: (g) => g.description || '—', className: 'text-muted-foreground' },
    {
      header: 'Created',
      cell: (g) => (g.createdAt ? new Date(g.createdAt).toLocaleDateString() : '—'),
      className: 'text-muted-foreground',
    },
  ],
  renderForm: (g, onDone) => (
    <RegistryTypeForm
      entity={g}
      singular="customer group"
      tokenPlaceholder="enterprise"
      create={(req) => createCustomerGroup(req)}
      update={(token, req) => updateCustomerGroup(token, req)}
      onDone={onDone}
    />
  ),
  removeConfirm: (g) => `Delete customer group “${g.token}”? This cannot be undone.`,
  renderDetailExtra: (g) => (
    <MembershipPanel
      groupType="customergroup"
      groupToken={g.token}
      memberType="customer"
      memberSingular="customer"
      loadCandidates={() => listCustomers({ pageNumber: 1, pageSize: 1000 }).then((r) => r.results)}
    />
  ),
};

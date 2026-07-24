// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryTypeForm, groupColumns, type RegistryResource } from '@/components/registry';
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
  i18nKey: 'customerGroup',
  banner: 'customers',
  list: listCustomerGroups,
  load: getCustomerGroup,
  remove: deleteCustomerGroup,
  idOf: (g) => g.id,
  tokenOf: (g) => g.token,
  nameOf: (g) => g.name,
  columns: groupColumns<CustomerGroup>(),
  renderForm: (g, onDone) => (
    <RegistryTypeForm
      entity={g}
      i18nKey="customerGroup"
      entityType="customer-group"
      create={(req) => createCustomerGroup(req)}
      update={(token, req) => updateCustomerGroup(token, req)}
      onDone={onDone}
    />
  ),
  detailExtraLabel: 'common:membersTab',
  renderDetailExtra: (g) => (
    <MembershipPanel
      groupType="group"
      groupToken={g.token}
      memberType="customer"
      memberI18nKey="customer"
      loadCandidates={() => listCustomers({ pageNumber: 1, pageSize: 1000 }).then((r) => r.results)}
    />
  ),
};

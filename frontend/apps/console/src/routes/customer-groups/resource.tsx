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
// Groups of every family are the one uniform EntityGroup, so the projection
// that keeps an edit from erasing its unedited fields is shared too.

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
      // 🔴 THE REQUEST NAMES ONLY WHAT THIS FORM EDITS, which is now the whole rule
      // rather than a hazard to work around. An edit used to have to re-send the
      // group's appearance and metadata (through groupPreserved) because a group
      // update replaced every field it named and omitting one erased it. The update
      // is partial now, so an unnamed field is left alone — and re-sending a value
      // the operator never saw would be a write over whatever it has become since.
      update={(token, req) =>
        updateCustomerGroup(token, { name: req.name, description: req.description })
      }
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

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
import { groupPreserved } from '@/lib/api/device-management';

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
      // RegistryTypeForm calls update only when editing, so g is set. A group
      // update replaces every field it names, so an edit that sent only the name
      // erased the group's appearance and its metadata. (Its membership mode and
      // selector survive — the server never takes those from a request that omits
      // them — so a dynamic group kept working while its metadata did not.)
      update={(token, req) =>
        updateCustomerGroup(token, { ...groupPreserved(g!), name: req.name, description: req.description })
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

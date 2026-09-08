// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryInstanceForm, tokenColumn, nameColumn, createdColumn, type RegistryResource } from '@/components/registry';
import { TypeCapsule, appearanceOf } from '@/components/TypeCapsule';
import { EntityAttributesPanel } from '@/components/EntityAttributesPanel';
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
        // A partial update: this form edits the name, the description and the type,
        // so it sends exactly those. externalId and metadata are untouched because
        // they are not mentioned — where the full-replace shape needed them re-sent
        // from a stale snapshot to survive at all.
        updateCustomer(token, {
          name: req.name,
          description: req.description,
          customerTypeToken: req.typeToken,
        })
      }
      onDone={onDone}
    />
  ),
  // Facet values live on the entity as EntityAttribute rows, and until this tab
  // existed nothing we ship could write one — the Browse axis for this family was
  // declarable and permanently unmatchable. The panel is shared with the other three
  // member families; only the entity type differs.
  detailTabs: [
    {
      value: 'facets',
      label: 'facets:panelTab',
      render: (c) => (
        <EntityAttributesPanel entityType="customer" entityToken={c.token} />
      ),
    },
  ],
};

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the device-management service.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-management';
import type {
  CustomersQuery,
  CustomerTypesQuery,
  CustomerTypeCreateRequest,
  CustomerCreateRequest,
} from '@/gql/device-management/graphql';
import {
  listEntityGroups,
  getEntityGroupOfType,
  createEntityGroup,
  updateEntityGroup,
  deleteEntityGroup,
  type EntityGroup,
  type GroupFormRequest,
} from './device-management';

// Public types are derived from the generated operation results so they always
// reflect the actual selection sets and can never drift from the schema.
export type Customer = CustomersQuery['customers']['results'][number];
export type CustomerType = CustomerTypesQuery['customerTypes']['results'][number];
export type Pagination = CustomersQuery['customers']['pagination'];
export type CustomerSearchResults = CustomersQuery['customers'];
export type CustomerTypeSearchResults = CustomerTypesQuery['customerTypes'];
// Customer groups are the uniform EntityGroup filtered to memberType='customer' (ADR-061).
export type CustomerGroup = EntityGroup;

// Re-export the generated request inputs so forms can type their request objects
// without reaching into the generated module directly.
export type { CustomerTypeCreateRequest, CustomerCreateRequest };

// ── Customers ───────────────────────────────────────────────────────────

const CUSTOMERS = graphql(`
  query Customers($criteria: CustomerSearchCriteria!) {
    customers(criteria: $criteria) {
      results {
        id
        token
        name
        description
        metadata
        createdAt
        customerType {
          id
          token
          name
          icon
          backgroundColor
          foregroundColor
          borderColor
        }
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

export async function listCustomers(opts: {
  pageNumber: number;
  pageSize: number;
}): Promise<CustomerSearchResults> {
  const data = await gql('device-management', CUSTOMERS, {
    criteria: {
      pageNumber: opts.pageNumber,
      pageSize: opts.pageSize,
    },
  });
  return data.customers;
}

const CUSTOMER_BY_TOKEN = graphql(`
  query CustomerByToken($tokens: [String!]!) {
    customersByToken(tokens: $tokens) {
      id
      token
      name
      description
      metadata
      createdAt
      customerType {
        id
        token
        name
        icon
        backgroundColor
        foregroundColor
        borderColor
      }
    }
  }
`);

export async function getCustomer(token: string): Promise<Customer | null> {
  const data = await gql('device-management', CUSTOMER_BY_TOKEN, { tokens: [token] });
  return data.customersByToken[0] ?? null;
}

const CREATE_CUSTOMER = graphql(`
  mutation CreateCustomer($request: CustomerCreateRequest) {
    createCustomer(request: $request) {
      id
      token
      name
      description
      metadata
      createdAt
      customerType {
        id
        token
        name
        icon
        backgroundColor
        foregroundColor
        borderColor
      }
    }
  }
`);

export async function createCustomer(request: CustomerCreateRequest): Promise<Customer> {
  const data = await gql('device-management', CREATE_CUSTOMER, { request });
  return data.createCustomer;
}

const UPDATE_CUSTOMER = graphql(`
  mutation UpdateCustomer($token: String!, $request: CustomerCreateRequest) {
    updateCustomer(token: $token, request: $request) {
      id
      token
      name
      description
      metadata
      createdAt
      customerType {
        id
        token
        name
        icon
        backgroundColor
        foregroundColor
        borderColor
      }
    }
  }
`);

// 🔴 `Required<…>` is not decoration: it makes the OMISSION a compile error.
// The update is a full replace, so the defect this whole file guards against is a
// caller that simply does not mention a field. Typing the parameter as the plain
// request — where every field is optional — is what let that compile. Requiring
// every key forces each caller to say what it wants done with each one, and
// `…Preserved(entity)` is how it says "leave this as it was".
export async function updateCustomer(
  token: string,
  request: Required<CustomerCreateRequest>,
): Promise<Customer> {
  const data = await gql('device-management', UPDATE_CUSTOMER, { token, request });
  return data.updateCustomer;
}

// 🔴 A customer update is a FULL REPLACE: the stored record is rebuilt from the
// request, so a field the request omits is DELETED. Every edit therefore starts
// from this projection of what the entity already is, and overrides only what the
// form edits. The `Required<…>` RETURN TYPE is the gate that keeps it honest — add a
// field to the schema and codegen widens the request type, which breaks this
// function until someone decides what an edit should do with it. Without that,
// the new field would simply start being erased, silently and successfully.
//
// customerTypeToken is deliberately NOT preserved here: it is a required field of the
// form, so every caller supplies it and the request type refuses to compile
// without it. Defaulting it would only be a way to send a wrong one.
export function customerPreserved(c: Customer): Required<Omit<CustomerCreateRequest, 'customerTypeToken'>> {
  // `?? null` rather than `?? undefined`: for a full replace an explicit null and
  // an omitted field both land as a nil *string server-side, and null is the one
  // that says so out loud.
  return {
    token: c.token,
    name: c.name ?? null,
    description: c.description ?? null,
    metadata: c.metadata ?? null,
  };
}

const DELETE_CUSTOMER = graphql(`
  mutation DeleteCustomer($token: String!) {
    deleteCustomer(token: $token)
  }
`);

export async function deleteCustomer(token: string): Promise<boolean> {
  const data = await gql('device-management', DELETE_CUSTOMER, { token });
  return data.deleteCustomer;
}

// ── Customer types ────────────────────────────────────────────────────────

const CUSTOMER_TYPES = graphql(`
  query CustomerTypes($criteria: CustomerTypeSearchCriteria!) {
    customerTypes(criteria: $criteria) {
      results {
        id
        token
        name
        description
        icon
        backgroundColor
        foregroundColor
        borderColor
        imageUrl
        metadata
        createdAt
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

export async function listCustomerTypes(opts: {
  pageNumber: number;
  pageSize: number;
}): Promise<CustomerTypeSearchResults> {
  const data = await gql('device-management', CUSTOMER_TYPES, {
    criteria: {
      pageNumber: opts.pageNumber,
      pageSize: opts.pageSize,
    },
  });
  return data.customerTypes;
}

// The customer-type getter and mutations select the same shape as the CustomerTypes
// query so their results stay assignable to the shared CustomerType type.
const CUSTOMER_TYPE_BY_TOKEN = graphql(`
  query CustomerTypeByToken($tokens: [String!]!) {
    customerTypesByToken(tokens: $tokens) {
      id
      token
      name
      description
      icon
      backgroundColor
      foregroundColor
      borderColor
      imageUrl
      metadata
      createdAt
    }
  }
`);

export async function getCustomerType(token: string): Promise<CustomerType | null> {
  const data = await gql('device-management', CUSTOMER_TYPE_BY_TOKEN, { tokens: [token] });
  return data.customerTypesByToken[0] ?? null;
}

const CREATE_CUSTOMER_TYPE = graphql(`
  mutation CreateCustomerType($request: CustomerTypeCreateRequest) {
    createCustomerType(request: $request) {
      id
      token
      name
      description
      icon
      backgroundColor
      foregroundColor
      borderColor
      imageUrl
      metadata
      createdAt
    }
  }
`);

export async function createCustomerType(request: CustomerTypeCreateRequest): Promise<CustomerType> {
  const data = await gql('device-management', CREATE_CUSTOMER_TYPE, { request });
  return data.createCustomerType;
}

const UPDATE_CUSTOMER_TYPE = graphql(`
  mutation UpdateCustomerType($token: String!, $request: CustomerTypeCreateRequest) {
    updateCustomerType(token: $token, request: $request) {
      id
      token
      name
      description
      icon
      backgroundColor
      foregroundColor
      borderColor
      imageUrl
      metadata
      createdAt
    }
  }
`);

export async function updateCustomerType(
  token: string,
  request: Required<CustomerTypeCreateRequest>,
): Promise<CustomerType> {
  const data = await gql('device-management', UPDATE_CUSTOMER_TYPE, { token, request });
  return data.updateCustomerType;
}

// The customer-type counterpart of customerPreserved: a customer-type update is a full
// replace too, and this form edits only name + description while the Appearance
// tab edits only icon + colors. Each starts from here so the other's fields — and
// the imageUrl and metadata no console form edits at all — survive the save.
export function customerTypePreserved(ct: CustomerType): Required<CustomerTypeCreateRequest> {
  return {
    token: ct.token,
    name: ct.name ?? null,
    description: ct.description ?? null,
    icon: ct.icon ?? null,
    backgroundColor: ct.backgroundColor ?? null,
    foregroundColor: ct.foregroundColor ?? null,
    borderColor: ct.borderColor ?? null,
    imageUrl: ct.imageUrl ?? null,
    metadata: ct.metadata ?? null,
  };
}

const DELETE_CUSTOMER_TYPE = graphql(`
  mutation DeleteCustomerType($token: String!) {
    deleteCustomerType(token: $token)
  }
`);

export async function deleteCustomerType(token: string): Promise<boolean> {
  const data = await gql('device-management', DELETE_CUSTOMER_TYPE, { token });
  return data.deleteCustomerType;
}
// ── Customer groups (memberType = 'customer') ───────────────────────────────
// Thin wrappers over the uniform EntityGroup operations (ADR-061), baking in the
// customer member family. See device-management.ts for the canonical group ops.

export const listCustomerGroups = (opts: { pageNumber: number; pageSize: number }) =>
  listEntityGroups({ ...opts, memberType: 'customer' });
export const getCustomerGroup = (token: string) => getEntityGroupOfType(token, 'customer');
export const createCustomerGroup = (request: GroupFormRequest) =>
  createEntityGroup({ ...request, memberType: 'customer' });
export const updateCustomerGroup = (token: string, request: Required<GroupFormRequest>) =>
  updateEntityGroup(token, { ...request, memberType: 'customer' });
export const deleteCustomerGroup = deleteEntityGroup;

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the device-management service.
import { gql } from '@/lib/graphql/client';
import { graphql } from '@/gql/device-management';
import type {
  CustomersQuery,
  CustomerTypesQuery,
  CustomerTypeCreateRequest,
  CustomerCreateRequest,
  CustomerGroupsQuery,
  CustomerGroupCreateRequest,
} from '@/gql/device-management/graphql';

// Public types are derived from the generated operation results so they always
// reflect the actual selection sets and can never drift from the schema.
export type Customer = CustomersQuery['customers']['results'][number];
export type CustomerType = CustomerTypesQuery['customerTypes']['results'][number];
export type Pagination = CustomersQuery['customers']['pagination'];
export type CustomerSearchResults = CustomersQuery['customers'];
export type CustomerTypeSearchResults = CustomerTypesQuery['customerTypes'];
export type CustomerGroup = CustomerGroupsQuery['customerGroups']['results'][number];
export type CustomerGroupSearchResults = CustomerGroupsQuery['customerGroups'];

// Re-export the generated request inputs so forms can type their request objects
// without reaching into the generated module directly.
export type { CustomerTypeCreateRequest, CustomerCreateRequest, CustomerGroupCreateRequest };

// ── Customers ───────────────────────────────────────────────────────────

const CUSTOMERS = graphql(`
  query Customers($criteria: CustomerSearchCriteria!) {
    customers(criteria: $criteria) {
      results {
        id
        token
        name
        description
        createdAt
        customerType {
          id
          token
          name
          backgroundColor
          foregroundColor
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
      createdAt
      customerType {
        id
        token
        name
        backgroundColor
        foregroundColor
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
      createdAt
      customerType {
        id
        token
        name
        backgroundColor
        foregroundColor
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
      createdAt
      customerType {
        id
        token
        name
        backgroundColor
        foregroundColor
      }
    }
  }
`);

export async function updateCustomer(
  token: string,
  request: CustomerCreateRequest,
): Promise<Customer> {
  const data = await gql('device-management', UPDATE_CUSTOMER, { token, request });
  return data.updateCustomer;
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
      createdAt
    }
  }
`);

export async function updateCustomerType(
  token: string,
  request: CustomerTypeCreateRequest,
): Promise<CustomerType> {
  const data = await gql('device-management', UPDATE_CUSTOMER_TYPE, { token, request });
  return data.updateCustomerType;
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

// ── Customer groups ───────────────────────────────────────────────────────

const CUSTOMER_GROUPS = graphql(`
  query CustomerGroups($criteria: CustomerGroupSearchCriteria!) {
    customerGroups(criteria: $criteria) {
      results {
        id
        token
        name
        description
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

export async function listCustomerGroups(opts: {
  pageNumber: number;
  pageSize: number;
}): Promise<CustomerGroupSearchResults> {
  const data = await gql('device-management', CUSTOMER_GROUPS, {
    criteria: {
      pageNumber: opts.pageNumber,
      pageSize: opts.pageSize,
    },
  });
  return data.customerGroups;
}

// The customer-group getter and mutations select the same shape as the CustomerGroups
// query so their results stay assignable to the shared CustomerGroup type.
const CUSTOMER_GROUP_BY_TOKEN = graphql(`
  query CustomerGroupByToken($tokens: [String!]!) {
    customerGroupsByToken(tokens: $tokens) {
      id
      token
      name
      description
      createdAt
    }
  }
`);

export async function getCustomerGroup(token: string): Promise<CustomerGroup | null> {
  const data = await gql('device-management', CUSTOMER_GROUP_BY_TOKEN, { tokens: [token] });
  return data.customerGroupsByToken[0] ?? null;
}

const CREATE_CUSTOMER_GROUP = graphql(`
  mutation CreateCustomerGroup($request: CustomerGroupCreateRequest) {
    createCustomerGroup(request: $request) {
      id
      token
      name
      description
      createdAt
    }
  }
`);

export async function createCustomerGroup(
  request: CustomerGroupCreateRequest,
): Promise<CustomerGroup> {
  const data = await gql('device-management', CREATE_CUSTOMER_GROUP, { request });
  return data.createCustomerGroup;
}

const UPDATE_CUSTOMER_GROUP = graphql(`
  mutation UpdateCustomerGroup($token: String!, $request: CustomerGroupCreateRequest) {
    updateCustomerGroup(token: $token, request: $request) {
      id
      token
      name
      description
      createdAt
    }
  }
`);

export async function updateCustomerGroup(
  token: string,
  request: CustomerGroupCreateRequest,
): Promise<CustomerGroup> {
  const data = await gql('device-management', UPDATE_CUSTOMER_GROUP, { token, request });
  return data.updateCustomerGroup;
}

const DELETE_CUSTOMER_GROUP = graphql(`
  mutation DeleteCustomerGroup($token: String!) {
    deleteCustomerGroup(token: $token)
  }
`);

export async function deleteCustomerGroup(token: string): Promise<boolean> {
  const data = await gql('device-management', DELETE_CUSTOMER_GROUP, { token });
  return data.deleteCustomerGroup;
}

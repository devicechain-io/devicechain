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
} from '@/gql/device-management/graphql';

// Public types are derived from the generated operation results so they always
// reflect the actual selection sets and can never drift from the schema.
export type Customer = CustomersQuery['customers']['results'][number];
export type CustomerType = CustomerTypesQuery['customerTypes']['results'][number];
export type Pagination = CustomersQuery['customers']['pagination'];
export type CustomerSearchResults = CustomersQuery['customers'];
export type CustomerTypeSearchResults = CustomerTypesQuery['customerTypes'];

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

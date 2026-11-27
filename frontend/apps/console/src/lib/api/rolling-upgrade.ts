// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Recognising an older service's refusal of something newer than it.
//
// During a rolling upgrade the console is the new release while a service's pods are a mix of
// the old and the new one, and any request can land on either. An old pod's validator refuses a
// whole document that names a field it does not define. Checking the schema first cannot avoid
// that: the check and the request are two requests and can reach different pods. So an operation
// that uses a newer field sends it, and on the exact refusal of that field sends the older shape.
//
// The refusal arrives in one of two forms, and both are read here:
// - the server's own validation error, in the GraphQL `errors` of the response;
// - with schema negotiation on (@devicechain/client enableSchemaNegotiation), the client's
//   refusal before sending, a SchemaSkewError of kind `unsupported` whose message carries the
//   same validator wording.
// Any other error that merely names the field (a resolver failure, a refused permission) is not
// one, so the patterns name the exact wording of a missing field or input field.

import { GraphQLRequestError, isSchemaSkewError } from '@devicechain/client';

/** The validator's wording for a selected field the type does not define. */
export function unservedField(type: string, field: string): RegExp {
  return new RegExp(`Cannot query field "${field}" on type "${type}"`);
}

/**
 * The validator's wording for an input field the input type does not define: the server's
 * (graphql-go), or the client's under negotiation (graphql-js).
 */
export function unservedInputField(type: string, field: string): RegExp {
  return new RegExp(
    `Field "${field}" is not defined by type "${type}"|Expected value of type "${type}!?" not to include unknown field "${field}"`,
  );
}

/** True when err is an older server's refusal matching one of refusals. */
export function refusedAsUnserved(err: unknown, refusals: RegExp[]): boolean {
  const matches = (message: string) => refusals.some((refusal) => refusal.test(message));
  if (isSchemaSkewError(err)) return err.kind === 'unsupported' && matches(err.message);
  return err instanceof GraphQLRequestError && !!err.errors?.some(({ message }) => matches(message));
}

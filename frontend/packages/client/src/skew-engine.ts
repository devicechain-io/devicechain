// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The checking engine behind schema negotiation. Internal: it is the only module that imports
// `graphql`, and skew.ts loads it with a dynamic import so a page that never negotiates never
// downloads it. Nothing here is exported from the package.

import * as gq from 'graphql';

type ValidateInputValue = (
  value: unknown,
  type: gq.GraphQLInputType,
  onError: (error: Error, path: readonly (string | number)[]) => void,
) => void;
type CoerceInputValue16 = (
  value: unknown,
  type: gq.GraphQLInputType,
  onError: (path: readonly (string | number)[], invalid: unknown, error: Error) => void,
) => unknown;

/** One reason an operation cannot be served. `missing` marks a thing the instance does not define. */
export interface Finding {
  message: string;
  missing: boolean;
}

interface DocumentVerdict {
  findings: Finding[];
  /** The single operation's variable definitions, for coercing the variables actually sent. */
  variables: { name: string; type: gq.GraphQLInputType }[];
}

/** A parsed contract with its per-document verdicts memoized. */
export interface Contract {
  schema: gq.GraphQLSchema;
  documents: Map<string, DocumentVerdict | null>;
}

// What graphql-js says when a name is not defined by the instance's schema, across its 16 and 17
// majors. Every other message (a required argument, a wrong type, a bad value) is a defect in the
// request for this instance, not evidence that the instance is older.
const MISSING = [
  /^Cannot query field /,
  /^Unknown argument /,
  /^Unknown type /,
  /^Unknown directive /,
  /is not defined by type /,
  /not to include unknown field /,
];

function finding(message: string): Finding {
  return { message, missing: MISSING.some((re) => re.test(message)) };
}

/** Parse the served SDL. Throws when it is not a valid schema. */
export function loadContract(sdl: string): Contract {
  return { schema: gq.buildSchema(sdl), documents: new Map() };
}

function documentVerdict(contract: Contract, query: string): DocumentVerdict | null {
  if (contract.documents.has(query)) return contract.documents.get(query) ?? null;
  let verdict: DocumentVerdict | null = null;
  try {
    const document = gq.parse(query);
    const findings = gq.validate(contract.schema, document).map((e) => finding(e.message));
    const operations = document.definitions.filter(
      (d): d is gq.OperationDefinitionNode => d.kind === gq.Kind.OPERATION_DEFINITION,
    );
    const variables: DocumentVerdict['variables'] = [];
    if (operations.length === 1) {
      for (const def of operations[0].variableDefinitions ?? []) {
        const type = gq.typeFromAST(contract.schema, def.type);
        if (type && gq.isInputType(type)) variables.push({ name: def.variable.name.value, type });
      }
    }
    verdict = { findings, variables };
  } catch {
    // A document this client cannot parse is the caller's defect, not skew: leave it to the
    // server to answer rather than inventing a verdict.
    verdict = null;
  }
  contract.documents.set(query, verdict);
  return verdict;
}

/**
 * Check one operation against a contract. Returns every reason it cannot be served (empty when it
 * can). The document verdict is memoized; the variables are checked on every call, in the JSON
 * shape that is sent, so a nested input field the instance lacks is reported and never dropped.
 */
export function findProblems(contract: Contract, query: string, variables: unknown): Finding[] {
  const verdict = documentVerdict(contract, query);
  if (!verdict) return [];
  const problems = [...verdict.findings];
  if (problems.length > 0) return problems;

  const sent = JSON.parse(JSON.stringify(variables ?? {})) as Record<string, unknown>;
  for (const { name, type } of verdict.variables) {
    if (sent[name] === undefined) continue;
    const report = (path: readonly (string | number)[], error: Error) => {
      problems.push(finding(`variable $${name}${path.length ? `.${path.join('.')}` : ''}: ${error.message}`));
    };
    // The peer range spans two majors. 17 validates with validateInputValue(value, type,
    // onError(error, path)); 16 has no such export and reports through coerceInputValue(value,
    // type, onError(path, invalid, error)). Both refuse a field the input type does not define.
    // Static member reads (not a dynamic lookup) so a bundler can still drop the rest of graphql.
    const validateInput = (gq as { validateInputValue?: ValidateInputValue }).validateInputValue;
    if (typeof validateInput === 'function') {
      validateInput(sent[name], type, (error, path) => report(path, error));
    } else {
      const coerce = (gq as { coerceInputValue?: CoerceInputValue16 }).coerceInputValue;
      if (typeof coerce !== 'function') throw new Error('the installed graphql cannot validate input values');
      coerce(sent[name], type, (path, _invalid, error) => report(path, error));
    }
  }
  return problems;
}

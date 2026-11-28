// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The console against a service one release older, as during a rolling upgrade. Each area's
// served schema is built twice: as it is, and with what this release added removed (the release
// before it). A fake server validates every request, document AND variables, against the schema
// it is running and refuses what it does not define, the way the real one does; the operations
// must then work against both.

import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import {
  buildASTSchema,
  getVariableValues,
  Kind,
  parse,
  validate,
  visit,
  type ASTNode,
  type GraphQLSchema,
  type OperationDefinitionNode,
} from 'graphql';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { GraphQLRequestError, SchemaSkewError } from '@devicechain/client';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));
vi.mock('@devicechain/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@devicechain/client')>()),
  gql: gqlMock,
}));

import { getLatestMeasurements } from './device-state';
import { createDetectionRule, updateDetectionRule, RuleDoorUnavailableError } from './device-management';
import { validateDetectionRule } from './event-processing';
import { refusedAsUnserved, unservedField, unservedInputField } from './rolling-upgrade';

function sdl(area: string): string {
  return readFileSync(resolve(process.cwd(), `../../../backend/services/${area}/graphql/schema.graphql`), 'utf8');
}

function ownerName(ancestors: readonly (ASTNode | readonly ASTNode[])[], kind: Kind): string | undefined {
  for (let i = ancestors.length - 1; i >= 0; i--) {
    const a = ancestors[i];
    if (!Array.isArray(a) && 'kind' in a && a.kind === kind && 'name' in a && a.name) return a.name.value;
  }
  return undefined;
}

// What this release added to each area, removed. Every removal is counted, so a renamed type
// or field fails here instead of leaving an "old" schema identical to the new one.
function before(area: string): GraphQLSchema {
  let removed = 0;
  const drop = () => {
    removed++;
    return null;
  };
  const ast = visit(parse(sdl(area)), {
    FieldDefinition(node, _k, _p, _path, ancestors) {
      const owner = ownerName(ancestors, Kind.OBJECT_TYPE_DEFINITION);
      if (area === 'device-state' && owner === 'LatestMeasurement' && node.name.value === 'textValue') return drop();
      if (area === 'device-management' && node.name.value === 'authoredVia') return drop();
      return undefined;
    },
    InputValueDefinition(node, _k, _p, _path, ancestors) {
      if (area === 'device-management' && node.name.value === 'authoredVia') return drop();
      if (area === 'event-processing' && node.name.value === 'metrics') {
        if (ownerName(ancestors, Kind.FIELD_DEFINITION) === 'validateDetectionRules') return drop();
      }
      return undefined;
    },
    EnumTypeDefinition(node) {
      return area === 'device-management' && node.name.value === 'AuthoredVia' ? drop() : undefined;
    },
    InputObjectTypeDefinition(node) {
      return area === 'event-processing' && node.name.value === 'DetectionMetricInput' ? drop() : undefined;
    },
  });
  const want = { 'device-state': 1, 'device-management': 5, 'event-processing': 2 }[area];
  if (removed !== want) throw new Error(`${area}: removed ${removed} definitions, want ${want}`);
  return buildASTSchema(ast);
}

const AREAS = ['device-state', 'device-management', 'event-processing'] as const;
const current = Object.fromEntries(AREAS.map((a) => [a, buildASTSchema(parse(sdl(a)))]));
const older = Object.fromEntries(AREAS.map((a) => [a, before(a)]));

const ANSWERS: Record<string, unknown> = {
  latestMeasurements: [
    { id: '1', name: 'temp', value: 21.5, occurredTime: 't' },
    { id: '2', name: 'mode', value: null, occurredTime: 't' },
  ],
  createDetectionRule: { id: '1', token: 'r' },
  updateDetectionRule: { id: '1', token: 'r' },
  validateDetectionRules: { valid: true, errors: [], warnings: [] },
};

// serve answers a request the way a server running schemas does: validation first, refusing with
// the validator's messages in `errors`.
function serve(schemas: Record<string, GraphQLSchema>) {
  gqlMock.mockImplementation(async (area: string, document: unknown, variables: Record<string, unknown>) => {
    const schema = schemas[area];
    const ast = parse(String(document));
    const errors = validate(schema, ast).map((e) => ({ message: e.message }));
    if (errors.length === 0) {
      const op = ast.definitions.find((d): d is OperationDefinitionNode => d.kind === Kind.OPERATION_DEFINITION)!;
      const coerced = getVariableValues(schema, op.variableDefinitions ?? [], variables ?? {});
      if ('errors' in coerced && coerced.errors) errors.push(...coerced.errors.map((e) => ({ message: e.message })));
    }
    if (errors.length > 0) throw new GraphQLRequestError(errors[0].message, 200, errors);
    const field = (ast.definitions[0] as OperationDefinitionNode).selectionSet.selections[0];
    const name = field.kind === Kind.FIELD ? field.name.value : '';
    const answer = name === 'latestMeasurements' && String(document).includes('textValue')
      ? (ANSWERS.latestMeasurements as object[]).map((m, i) => ({ ...m, textValue: i === 1 ? 'eco' : null }))
      : ANSWERS[name];
    return { [name]: answer };
  });
}

const rule = { deviceProfileToken: 'p', name: 'n', description: null, definition: '{}', enabled: true };

beforeEach(() => {
  gqlMock.mockReset();
});

describe.each([
  ['current', current, 1],
  ['one release older', older, 2],
] as const)('against a %s server', (label, schemas, requests) => {
  const upToDate = label === 'current';
  beforeEach(() => serve(schemas));

  it('reads the latest readings, and says when text readings are not served yet', async () => {
    const got = await getLatestMeasurements('d1');
    expect(gqlMock).toHaveBeenCalledTimes(requests);
    expect(got.readings.map((m) => m.value)).toEqual([21.5, null]);
    expect(got.textServed).toBe(upToDate);
    expect(got.readings[1].textValue).toBe(upToDate ? 'eco' : null);
  });

  it.each(['FORM', 'CANVAS'] as const)('saves a %s rule, with its door when the server takes it', async (door) => {
    await updateDetectionRule('r', { ...rule, authoredVia: door });
    await createDetectionRule({ token: 'r', ...rule, authoredVia: door });
    expect(gqlMock).toHaveBeenCalledTimes(2 * requests);
    const sent = gqlMock.mock.calls.map((c) => c[2].request);
    expect(sent[sent.length - 1]).toEqual(upToDate ? { token: 'r', ...rule, authoredVia: door } : { token: 'r', ...rule });
  });

  it('saves a Describe-drafted rule only where its door is recorded', async () => {
    const save = createDetectionRule({ token: 'r', ...rule, authoredVia: 'DESCRIBE' });
    if (upToDate) await save;
    else await expect(save).rejects.toBeInstanceOf(RuleDoorUnavailableError);
    // Never a second request without the door.
    expect(gqlMock).toHaveBeenCalledTimes(1);
  });

  it('validates a rule on a profile with a STRING metric', async () => {
    const got = await validateDetectionRule('r', '{}', false, [{ key: 'mode', dataType: 'STRING' }]);
    expect(got.ok).toBe(true);
    expect(gqlMock).toHaveBeenCalledTimes(requests);
  });
});

describe('refusedAsUnserved', () => {
  const refusals = [
    unservedField('LatestMeasurement', 'textValue'),
    unservedInputField('DetectionRuleUpdateRequest', 'authoredVia'),
  ];
  const asServer = (message: string) => new GraphQLRequestError(message, 200, [{ message }]);

  it.each([
    // graphql-go, as the service validates.
    'Cannot query field "textValue" on type "LatestMeasurement".',
    'Variable "request" has invalid value.\nField "authoredVia" is not defined by type "DetectionRuleUpdateRequest".',
    // graphql-js, as the client validates under schema negotiation.
    'Expected value of type "DetectionRuleUpdateRequest!" not to include unknown field "authoredVia", found: {}.',
  ])('recognises %s', (message) => {
    expect(refusedAsUnserved(asServer(message), refusals)).toBe(true);
  });

  it('recognises the refusal in any of the errors, and from schema negotiation', () => {
    const message = 'Cannot query field "textValue" on type "LatestMeasurement".';
    expect(refusedAsUnserved(new GraphQLRequestError('x', 200, [{ message: 'x' }, { message }]), refusals)).toBe(true);
    expect(refusedAsUnserved(new SchemaSkewError(`this instance does not serve what this console asked for: ${message}`, 'unsupported', '/api/device-state/graphql'), refusals)).toBe(true);
  });

  it.each([
    asServer('textValue resolver failed'),
    asServer('Forbidden: authoredVia'),
    asServer('Cannot query field "textValue" on type "Other".'),
    new GraphQLRequestError('Cannot query field "textValue" on type "LatestMeasurement".', 0),
    new SchemaSkewError('Cannot query field "textValue" on type "LatestMeasurement".', 'invalid-request', '/x'),
    new Error('Cannot query field "textValue" on type "LatestMeasurement".'),
  ])('does not take another failure for it (%#)', (err) => {
    expect(refusedAsUnserved(err, refusals)).toBe(false);
  });

  it('propagates any other failure without a second request', async () => {
    const err = asServer('Forbidden');
    gqlMock.mockRejectedValue(err);
    await expect(getLatestMeasurements('d1')).rejects.toBe(err);
    await expect(updateDetectionRule('r', { ...rule, authoredVia: 'FORM' })).rejects.toBe(err);
    await expect(validateDetectionRule('r', '{}', false, [{ key: 'mode', dataType: 'STRING' }])).rejects.toBe(err);
    expect(gqlMock).toHaveBeenCalledTimes(3);
  });
});

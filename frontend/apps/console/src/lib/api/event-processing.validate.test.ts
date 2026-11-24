// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// validateDetectionRule sends the profile's metric types only when a STRING metric exists, in a
// document of its own: a server that predates the argument rejects a document that declares a type
// it does not know, so every other profile must keep sending the plain one.

import { beforeEach, describe, expect, it, vi } from 'vitest';

const gql = vi.fn();
vi.mock('@devicechain/client', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  gql: (...a: unknown[]) => gql(...a),
}));

import { validateDetectionRule } from './event-processing';

const answer = { validateDetectionRules: { valid: true, errors: [], warnings: [] } };

beforeEach(() => {
  gql.mockReset();
  gql.mockResolvedValue(answer);
});

describe('validateDetectionRule metric types', () => {
  it('sends the types, in the document that declares them, when a STRING metric exists', async () => {
    await validateDetectionRule('r', '{}', false, [
      { key: 'mode', dataType: 'STRING' },
      { key: 't', dataType: 'DOUBLE' },
    ]);
    const [, doc, vars] = gql.mock.calls[0];
    expect(String(doc)).toContain('DetectionMetricInput');
    expect(vars.metrics).toHaveLength(2);
  });

  it('sends the plain document, with no metrics, when there is no STRING metric or no list', async () => {
    await validateDetectionRule('r', '{}', false, [{ key: 't', dataType: 'DOUBLE' }]);
    await validateDetectionRule('r', '{}');
    for (const [, doc, vars] of gql.mock.calls) {
      expect(String(doc)).not.toContain('DetectionMetricInput');
      expect(vars.metrics).toBeUndefined();
    }
  });
});

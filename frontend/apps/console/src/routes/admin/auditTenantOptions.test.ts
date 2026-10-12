// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, it, expect } from 'vitest';

import { tenantFilterOptions } from './auditTenantOptions';

const label = (token: string, at: string) => `${token} (deleted ${at.slice(0, 10)})`;

describe('tenantFilterOptions', () => {
  it('offers live tenants by token and completed deletions by their retained key', () => {
    const opts = tenantFilterOptions(
      ['acme', 'globex'],
      [
        {
          token: 'acme',
          retainedAuditTenant: 'acme~1700000000',
          completedAt: '2026-10-01T10:00:00Z',
        },
      ],
      label,
    );
    expect(opts).toEqual([
      { value: 'acme' },
      { value: 'globex' },
      { value: 'acme~1700000000', label: 'acme (deleted 2026-10-01)' },
    ]);
  });

  it('keeps two deletions of one token apart', () => {
    const opts = tenantFilterOptions(
      [],
      [
        { token: 'acme', retainedAuditTenant: 'acme~1', completedAt: '2026-01-01T00:00:00Z' },
        { token: 'acme', retainedAuditTenant: 'acme~2', completedAt: '2026-06-01T00:00:00Z' },
      ],
      label,
    );
    expect(opts.map((o) => o.value)).toEqual(['acme~1', 'acme~2']);
    expect(opts[0].label).not.toEqual(opts[1].label);
  });

  it('does not offer an in-flight deletion, whose entries are still under the token', () => {
    const opts = tenantFilterOptions(
      [],
      [{ token: 'acme', retainedAuditTenant: 'acme~1', completedAt: null }],
      label,
    );
    expect(opts).toEqual([]);
  });
});

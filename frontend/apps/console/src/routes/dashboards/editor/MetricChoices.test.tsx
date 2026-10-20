// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The dashboard editor's metric choices: the bound device's published metrics, with a
// calculated one marked so an author can tell it is computed, not reported.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

import { MetricChoices } from './MetricChoices';

afterEach(cleanup);
beforeEach(() => gqlMock.mockReset());

const metric = (metricKey: string, calculated: boolean) => ({
  metricKey,
  dataType: 'DOUBLE',
  unit: null,
  minValue: null,
  maxValue: null,
  allowedValues: null,
  calculated,
});

describe('MetricChoices', () => {
  it('marks a calculated metric and toggles keys in and out of the selection', async () => {
    gqlMock.mockResolvedValue({
      deviceMetricVocabulary: { metrics: [metric('volts', false), metric('power_kw', true)] },
    });
    const onChange = vi.fn();
    render(<MetricChoices deviceToken="dev-1" measurements={['volts']} onChange={onChange} />);

    const power = await screen.findByRole('button', { name: /power_kw/ });
    expect(power.textContent).toContain('Calculated');
    expect(screen.getByRole('button', { name: /volts/ }).textContent).not.toContain('Calculated');
    expect(screen.getByRole('button', { name: /volts/ }).getAttribute('aria-pressed')).toBe('true');

    fireEvent.click(power);
    expect(onChange).toHaveBeenLastCalledWith(['volts', 'power_kw']);
    fireEvent.click(screen.getByRole('button', { name: /volts/ }));
    expect(onChange).toHaveBeenLastCalledWith([]);
  });

  it('renders nothing for an unbound or unknown device', async () => {
    gqlMock.mockResolvedValue({ deviceMetricVocabulary: null });
    const { container, rerender } = render(<MetricChoices deviceToken="" measurements={[]} onChange={vi.fn()} />);
    rerender(<MetricChoices deviceToken="ghost" measurements={[]} onChange={vi.fn()} />);
    await waitFor(() => expect(gqlMock).toHaveBeenCalledTimes(1));
    expect(container.querySelector('ul')).toBeNull();
  });
});

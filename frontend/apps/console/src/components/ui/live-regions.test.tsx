// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/config';
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import { ErrorBanner } from '@/components/ui/error-banner';
import { ErrorState } from '@/components/ui/error-state';
import { LoadingState } from '@/components/ui/loading-state';

afterEach(() => cleanup());

describe('shared state live regions', () => {
  it('announces an error banner as an alert', () => {
    render(<ErrorBanner message="Request failed" />);

    expect(screen.getByRole('alert').textContent).toContain('Request failed');
  });

  it.each([undefined, 'Debug payload'])('announces an error state once (details: %s)', (details) => {
    render(<ErrorState description="Request failed" details={details} />);

    expect(screen.getAllByRole('alert')).toHaveLength(1);
    expect(screen.getByRole('alert').textContent).toContain('Request failed');
  });

  it.each([false, true])('announces loading as a status (spinner: %s)', (spinner) => {
    render(<LoadingState description="Loading devices" spinner={spinner} />);

    expect(screen.getAllByRole('status')).toHaveLength(1);
    expect(screen.getByRole('status').textContent).toContain('Loading devices');
  });
});

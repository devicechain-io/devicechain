// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/config';
import { cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it } from 'vitest';
import { OriginatorLink, formatAlarmValue, originatorDevicePath, originatorLabel } from './alarmDisplay';

afterEach(cleanup);

const dev = { originatorType: 'device', originatorId: '7', originatorToken: 'pump/1 a' };

describe('originatorLabel', () => {
  it('is the token, or type and id when the originator cannot be resolved', () => {
    expect(originatorLabel(dev)).toBe('pump/1 a');
    expect(originatorLabel({ ...dev, originatorToken: null })).toBe('device #7');
    expect(originatorLabel({ ...dev, originatorToken: undefined })).toBe('device #7');
  });
});

describe('originatorDevicePath', () => {
  it('is the device page, with the token escaped', () => {
    expect(originatorDevicePath(dev)).toBe('/devices/pump%2F1%20a');
  });
  it('accepts the type in either case', () => {
    expect(originatorDevicePath({ ...dev, originatorType: 'DEVICE' })).toBe('/devices/pump%2F1%20a');
  });
  it('is null for another kind of originator, or one that cannot be named', () => {
    expect(originatorDevicePath({ ...dev, originatorType: 'asset' })).toBeNull();
    expect(originatorDevicePath({ ...dev, originatorToken: null })).toBeNull();
    expect(originatorDevicePath({ ...dev, originatorToken: '' })).toBeNull();
  });
});

describe('OriginatorLink', () => {
  it('renders a link for a device and bare text otherwise', () => {
    const { rerender } = render(
      <MemoryRouter>
        <OriginatorLink alarm={dev} />
      </MemoryRouter>,
    );
    expect(screen.getByRole('link', { name: 'pump/1 a' }).getAttribute('href')).toBe('/devices/pump%2F1%20a');
    rerender(
      <MemoryRouter>
        <OriginatorLink alarm={{ ...dev, originatorType: 'asset' }} />
      </MemoryRouter>,
    );
    expect(screen.queryByRole('link')).toBeNull();
    expect(screen.getByText('pump/1 a')).toBeTruthy();
  });
});

describe('formatAlarmValue', () => {
  it('appends the unit when there is one', () => {
    expect(formatAlarmValue(21.5, '°C')).toBe('21.5 °C');
  });
  it('prints the bare value when there is no unit', () => {
    expect(formatAlarmValue(21.5)).toBe('21.5');
    expect(formatAlarmValue(21.5, '')).toBe('21.5');
    expect(formatAlarmValue(21.5, null)).toBe('21.5');
  });
  it('prints a dash, with no unit after it, when there is no value', () => {
    expect(formatAlarmValue(null, '°C')).toBe('—');
    expect(formatAlarmValue(undefined, '°C')).toBe('—');
  });
  it('rounds like a dashboard card', () => {
    expect(formatAlarmValue(16.24614270883714, 'bar')).toBe('16.246 bar');
  });
});

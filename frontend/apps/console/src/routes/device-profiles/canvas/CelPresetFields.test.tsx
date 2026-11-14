// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/config';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { CelPresetFields } from './CelPresetFields';
import { NodeInspector } from './inspector';
import '@/i18n/lazy/deviceProfiles';

afterEach(cleanup);

it('integrates with branch configuration while preserving authored CEL and other fields until apply', () => {
  const onChange = vi.fn();
  render(<NodeInspector type="branch" config={{ name: 'hot', when: 'hasValue && value > 99.0' }} onChange={onChange} />);
  expect((document.getElementById('cfg-branch-when') as HTMLTextAreaElement).value).toBe('hasValue && value > 99.0');
  fireEvent.click(screen.getByText('Build an expression'));
  expect(onChange).not.toHaveBeenCalled();
  fireEvent.change(screen.getByLabelText('Value'), { target: { value: '100' } });
  fireEvent.click(screen.getByRole('button', { name: 'Use expression' }));
  expect(onChange).toHaveBeenCalledWith({ name: 'hot', when: 'hasValue && value > 100.0' });
});

it('does not rewrite existing CEL until the author explicitly applies valid input', () => {
  const onApply = vi.fn();
  render(<CelPresetFields context="event" onApply={onApply} />);
  fireEvent.click(screen.getByText('Build an expression'));
  const apply = screen.getByRole('button', { name: 'Use expression' });
  expect((apply as HTMLButtonElement).disabled).toBe(true);
  fireEvent.change(screen.getByLabelText('Metric'), { target: { value: 'temp' } });
  fireEvent.change(screen.getByLabelText('Value'), { target: { value: '20' } });
  expect(onApply).not.toHaveBeenCalled();
  fireEvent.click(apply);
  expect(onApply).toHaveBeenCalledWith('"temp" in m && m["temp"] > 20.0');
});

it('offers Math without manufacturing a default operand', () => {
  const onApply = vi.fn();
  render(<CelPresetFields context="event" math onApply={onApply} />);
  fireEvent.click(screen.getByText('Build an expression'));
  fireEvent.change(screen.getByLabelText('Metric'), { target: { value: 'temp' } });
  const apply = screen.getByRole('button', { name: 'Use expression' });
  expect((apply as HTMLButtonElement).disabled).toBe(true);
  fireEvent.change(screen.getByLabelText('Value'), { target: { value: '1.8' } });
  fireEvent.click(apply);
  expect(onApply).toHaveBeenCalledWith('m["temp"] * 1.8');
});

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { CelPresetFields } from './CelPresetFields';
import { NodeInspector } from './inspector';
import '@/i18n/lazy/deviceProfiles';

afterEach(cleanup);

it('keeps labels and inputs scoped to each preset editor when several are mounted', () => {
  const onFirstApply = vi.fn();
  const onSecondApply = vi.fn();
  const { container } = render(<>
    <CelPresetFields context="event" onApply={onFirstApply} />
    <CelPresetFields context="event" onApply={onSecondApply} />
    <CelPresetFields context="event" math onApply={vi.fn()} />
  </>);
  const editors = Array.from(container.querySelectorAll('details'));
  editors.forEach((editor) => {
    fireEvent.click(within(editor).getByText('Build an expression'));
    editor.querySelectorAll('label').forEach((label) => {
      expect(label.htmlFor).not.toBe('');
      expect(document.getElementById(label.htmlFor)?.closest('details')).toBe(editor);
    });
  });
  const ids = Array.from(container.querySelectorAll('[id]'), (element) => element.id);
  expect(new Set(ids).size).toBe(ids.length);

  fireEvent.change(within(editors[1]).getByLabelText('Metric'), { target: { value: 'temp' } });
  fireEvent.change(within(editors[1]).getByLabelText('Value'), { target: { value: '20' } });
  fireEvent.click(within(editors[1]).getByRole('button', { name: 'Use expression' }));
  expect(onSecondApply).toHaveBeenCalledWith('"temp" in m && m["temp"] > 20.0');
  expect(onFirstApply).not.toHaveBeenCalled();
  expect((within(editors[0]).getByLabelText('Metric') as HTMLInputElement).value).toBe('');
});

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

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { StrictMode, type ReactNode } from 'react';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import axe from 'axe-core';
import { FormField } from './form-field';
import { Input } from './input';
import { Textarea } from './textarea';
import { Select, SelectItem } from './select';
import { Checkbox } from './checkbox';
import { Combobox } from './combobox';
import { AsyncCombobox } from './async-combobox';
import { MultiSelect } from './multi-select';
import { ColorPicker } from './color-picker';
import { FilePicker } from './file-picker';

afterEach(cleanup);

// Run axe in the workspace's existing Vitest gate. Layout/color contrast needs a
// real browser; these checks exercise the rendered control semantics in jsdom.
const audit = (container: HTMLElement) => axe.run(container, {
  rules: { 'color-contrast': { enabled: false }, region: { enabled: false } },
});

describe('FormField label association', () => {
  const controls: [string, () => ReactNode][] = [
    ['input', () => <Input />],
    ['textarea', () => <Textarea />],
    ['select', () => <Select value="a" onChange={() => {}}><SelectItem value="a">Alpha</SelectItem></Select>],
    ['checkbox', () => <Checkbox />],
    ['combobox', () => <Combobox options={[]} value="" onChange={() => {}} />],
    ['async combobox', () => <AsyncCombobox search={async () => ({ options: [] })} value="" onChange={() => {}} />],
    ['multi-select', () => <MultiSelect options={[]} value={[]} onChange={() => {}} />],
    ['color picker', () => <ColorPicker ariaLabel="Field" value="#112233" onChange={() => {}} />],
    ['file picker', () => <FilePicker onPick={() => {}}>Choose file</FilePicker>],
  ];

  it.each(controls)('associates a wrapped %s and passes axe', async (_, control) => {
    const { container } = render(<FormField label="Field"><div>{control()}</div></FormField>);
    const label = screen.getByText('Field', { selector: 'label' }) as HTMLLabelElement;
    const element = document.getElementById(label.htmlFor);
    expect(element).not.toBeNull();
    expect(label.control).toBe(element);
    expect(screen.getByLabelText('Field')).toBe(element);
    expect((await audit(container)).violations).toEqual([]);
  });

  it('proves the axe gate rejects an unassociated label', async () => {
    const { container } = render(<div><label>Field</label><Input /></div>);
    expect((await audit(container)).violations.map((v) => v.id)).toContain('label');
  });

  it('activates the checkbox when its visible label is clicked', () => {
    const onChange = vi.fn();
    render(<FormField label="Enabled"><Checkbox onCheckedChange={onChange} /></FormField>);
    fireEvent.click(screen.getByText('Enabled'));
    expect(onChange).toHaveBeenCalledWith(true);
  });

  it('announces both the field and the selected value of a dropdown', () => {
    render(<FormField label="Device type">
      <Combobox options={[{ value: 'sensor', label: 'Sensor' }]} value="sensor" onChange={() => {}} allowClear={false} />
    </FormField>);
    expect(screen.getByRole('button', { name: 'Device type Sensor' })).toBe(screen.getByLabelText('Device type'));
  });

  it('preserves explicit ids with and without htmlFor and outside a field', () => {
    render(<>
      <FormField label="Manual" htmlFor="manual"><Input id="manual" /></FormField>
      <FormField label="Existing"><Input id="existing" /></FormField>
      <Input id="standalone" aria-label="Standalone" />
    </>);
    expect(screen.getByLabelText('Manual').id).toBe('manual');
    expect(screen.getByLabelText('Existing').id).toBe('existing');
    expect(screen.getByLabelText('Standalone').id).toBe('standalone');
  });

  it('keeps generated ids unique and stable across rerenders in StrictMode', () => {
    const form = (value: string) => <StrictMode>
      <FormField label="First"><Input defaultValue={value} /></FormField>
      <FormField label="Second"><Input /></FormField>
    </StrictMode>;
    const { rerender } = render(form('a'));
    const first = screen.getByLabelText('First').id;
    const second = screen.getByLabelText('Second').id;
    expect(first).not.toBe(second);
    rerender(form('b'));
    expect(screen.getByLabelText('First').id).toBe(first);
    expect(screen.getByLabelText('Second').id).toBe(second);
  });

  it('does not duplicate ids in compound fields or leak across nested fields', () => {
    const { container } = render(<FormField label="Outer">
      <Input />
      <Input aria-label="Additional" />
      <FormField label="Inner"><Textarea /></FormField>
    </FormField>);
    const ids = [...container.querySelectorAll('[id]')].map((el) => el.id);
    expect(new Set(ids).size).toBe(ids.length);
    expect(screen.getByLabelText('Outer')).not.toBe(screen.getByLabelText('Additional'));
    expect(screen.getByLabelText('Inner').tagName).toBe('TEXTAREA');
  });

  it('keeps an auxiliary file picker independently named', () => {
    render(<FormField label="Certificate"><Textarea /><FilePicker onPick={() => {}}>Load file</FilePicker></FormField>);
    expect(screen.getByLabelText('Certificate').tagName).toBe('TEXTAREA');
    expect(screen.getByRole('button', { name: 'Load file' })).toBeTruthy();
  });

  it('reassociates when the mounted control changes', () => {
    const { rerender } = render(<FormField label="Field"><Input /></FormField>);
    const old = screen.getByLabelText('Field');
    rerender(<FormField label="Field"><Textarea /></FormField>);
    expect(screen.getByLabelText('Field').tagName).toBe('TEXTAREA');
    expect(screen.getByLabelText('Field')).not.toBe(old);
  });
});

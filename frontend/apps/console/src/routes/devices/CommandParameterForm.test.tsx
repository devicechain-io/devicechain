// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The command parameter form is a CONTROLLED form: the parent owns values and errors, this renders
// one control per declared parameter and reports each edit. These tests pin what each declared
// type renders, what the helper line says, that an error is shown under its field, that nothing
// is editable when disabled, and the three-state boolean the asset fact editor asks for.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { CommandParameter } from '@devicechain/dashboards';
import { CommandParameterForm } from './CommandParameterForm';

afterEach(cleanup);

const PARAMS: CommandParameter[] = [
  { name: 'setpoint', kind: 'SCALAR', dataType: 'DOUBLE', unit: 'C', minValue: 5, maxValue: 30, required: true, default: '21.5' },
  { name: 'retries', kind: 'SCALAR', dataType: 'INT', maxValue: 9 },
  { name: 'label', kind: 'SCALAR', dataType: 'STRING', description: 'Shown on the panel' },
  { name: 'enabled', kind: 'SCALAR', dataType: 'BOOLEAN' },
  { name: 'mode', kind: 'SCALAR', dataType: 'STRING', enum: ['eco', 'boost'] },
  { name: 'schedule', kind: 'OBJECT', parameters: [] },
];

function setup(over: Partial<Parameters<typeof CommandParameterForm>[0]> = {}) {
  const onChange = vi.fn();
  const utils = render(
    <CommandParameterForm params={PARAMS} values={{}} errors={{}} onChange={onChange} {...over} />,
  );
  return { onChange, ...utils };
}

describe('CommandParameterForm', () => {
  it('renders nothing for a command with no parameters', () => {
    const { container } = setup({ params: [] });
    expect(container.firstChild).toBeNull();
  });

  it('renders the control each declared type calls for, with its label and hint', () => {
    setup();
    // Required parameters carry the asterisk.
    expect(screen.getByText('setpoint *')).toBeTruthy();
    expect(screen.getByText('retries')).toBeTruthy();
    // Bounds and unit are folded into one muted helper line.
    expect(screen.getByText('in C · 5–30')).toBeTruthy();
    expect(screen.getByText('≤ 9')).toBeTruthy();
    expect(screen.getByText('Shown on the panel')).toBeTruthy();
    const numbers = document.querySelectorAll('input[type="number"]');
    expect(numbers).toHaveLength(2);
    // DOUBLE is unconstrained, INT steps by whole numbers.
    expect((numbers[0] as HTMLInputElement).step).toBe('any');
    expect((numbers[1] as HTMLInputElement).step).toBe('1');
    // The declared default shows as the placeholder, not as a value.
    expect((numbers[0] as HTMLInputElement).placeholder).toBe('21.5');
    expect((numbers[0] as HTMLInputElement).value).toBe('');
    expect(document.querySelectorAll('input[type="text"]')).toHaveLength(1);
    expect(screen.getAllByRole('checkbox')).toHaveLength(1);
    expect(screen.getByText('Select a value')).toBeTruthy();
  });

  it('says a structured parameter has no typed control instead of rendering a text box', () => {
    setup();
    expect(screen.getByText(/Structured parameter/)).toBeTruthy();
  });

  it('reports the exact string the operator typed, keyed by parameter name', () => {
    const { onChange } = setup();
    const numbers = document.querySelectorAll('input[type="number"]');
    fireEvent.change(numbers[0], { target: { value: '22.5' } });
    expect(onChange).toHaveBeenLastCalledWith('setpoint', '22.5');
    fireEvent.change(numbers[1], { target: { value: '3' } });
    expect(onChange).toHaveBeenLastCalledWith('retries', '3');
    fireEvent.change(document.querySelector('input[type="text"]') as HTMLInputElement, {
      target: { value: 'Boiler' },
    });
    expect(onChange).toHaveBeenLastCalledWith('label', 'Boiler');
  });

  it('shows the value it is given, and the checkbox follows it', () => {
    setup({ values: { setpoint: '19', enabled: 'true' } });
    expect((document.querySelectorAll('input[type="number"]')[0] as HTMLInputElement).value).toBe('19');
    expect(screen.getByRole('checkbox').getAttribute('aria-checked')).toBe('true');
  });

  it('serializes a ticked checkbox as true and an unticked one as false', () => {
    const { onChange, rerender } = setup();
    fireEvent.click(screen.getByRole('checkbox'));
    expect(onChange).toHaveBeenLastCalledWith('enabled', 'true');
    rerender(<CommandParameterForm params={PARAMS} values={{ enabled: 'true' }} errors={{}} onChange={onChange} />);
    fireEvent.click(screen.getByRole('checkbox'));
    expect(onChange).toHaveBeenLastCalledWith('enabled', 'false');
  });

  it('shows the parent\'s error under the field it belongs to', () => {
    setup({ errors: { setpoint: 'setpoint must be at most 30', mode: 'pick a mode' } });
    expect(screen.getByText('setpoint must be at most 30')).toBeTruthy();
    expect(screen.getByText('pick a mode')).toBeTruthy();
    expect(screen.queryByText(/retries must/)).toBeNull();
  });

  it('lets an enum be chosen from its declared values only', async () => {
    const { onChange } = setup();
    fireEvent.click(screen.getByText('Select a value'));
    expect(await screen.findByText('eco')).toBeTruthy();
    fireEvent.click(screen.getByText('boost'));
    expect(onChange).toHaveBeenLastCalledWith('mode', 'boost');
  });

  it('disables every control when disabled', () => {
    setup({ disabled: true });
    for (const el of document.querySelectorAll('input')) expect((el as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByRole('checkbox') as HTMLButtonElement).disabled).toBe(true);
  });

  it('offers unset / true / false for a boolean when asked, and unset for a never-set value', () => {
    const { onChange } = setup({ booleanTriState: true, values: { enabled: 'maybe' } });
    expect(screen.queryByRole('checkbox')).toBeNull();
    const unset = screen.getByRole('radio', { name: 'Unset' });
    expect(unset.getAttribute('aria-checked')).toBe('true');
    expect(screen.getByRole('radio', { name: 'True' }).getAttribute('aria-checked')).toBe('false');
    fireEvent.click(screen.getByRole('radio', { name: 'False' }));
    expect(onChange).toHaveBeenLastCalledWith('enabled', 'false');
  });
});

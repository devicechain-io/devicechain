// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Registered editors. The built-in workflows (command vocabulary, slot target, basemap) and a
// trusted package's own editor are written against ONE interface -- the SDK's
// WidgetEditorProps: the widget's options, a validated `setOptions`, and host services passed
// explicitly -- and these tests drive them through exactly that, with no API module mocked.

import '@/i18n/config';
import type { OptionSpec, OptionsOf, ResolvedWidgetInstance, SlotDefinition, WidgetOptionSpecs } from '@devicechain/dashboards';
import { createWidgetRegistry, defineWidget, type EditorCommandChoices, type WidgetEditorProps } from '@devicechain/widget-sdk';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { hostControl, type ControlEditorProps } from './controlEditors';
import { WidgetConfigPanel } from './WidgetConfigPanel';

vi.mock('./EntityPicker', () => ({ EntityPicker: () => null }));
// The panel's own command lookup would hit the API; an editor driven directly never calls it,
// and the panel-level tests below never open a command control.
vi.mock('@/lib/api/device-management', () => ({
  getDeviceCommandVocabulary: async () => null,
  listCommandDefinitionsForDevice: async () => [],
}));

afterEach(cleanup);

const COMMANDS: EditorCommandChoices = {
  constrained: true,
  selectable: [
    { commandKey: 'reboot', name: 'Reboot', parameterSchema: '[]' },
    { commandKey: 'setpoint', name: 'Set point', parameterSchema: '[{"name":"target","type":"FLOAT"}]' },
  ],
  draftOnly: ['calibrate'],
};

const cv = (extra: Partial<OptionSpec> = {}): OptionSpec => ({ kind: 'string', doc: '.', control: 'command-vocabulary', ...extra }) as OptionSpec;

function descriptorWith<const S extends WidgetOptionSpecs>(options: S) {
  return {
    id: 'acme/probe' as const,
    version: '1.0.0',
    sdkApiVersion: 1 as const,
    optionsVersion: 1,
    display: { title: 'Probe' },
    channel: 'none' as const,
    datasource: { mode: 'none' as const },
    binding: {},
    options,
    defaults: {},
    interactions: {},
  };
}

describe('the built-in command workflow as a registered editor', () => {
  const descriptor = descriptorWith({ commandName: cv(), commandLabel: cv(), parameterSchema: cv({ format: 'json' } as Partial<OptionSpec>) });

  function renderEditor(over: Partial<ControlEditorProps['services']>, options: Record<string, unknown> = {}) {
    const setOptions = vi.fn();
    const Editor = hostControl('command-vocabulary')!.Editor!;
    render(
      <Editor
        options={options}
        setOptions={setOptions}
        services={{ slots: {}, deviceToken: undefined, ...over }}
        descriptor={descriptor}
        optionKeys={['commandName', 'commandLabel', 'parameterSchema']}
      />,
    );
    return setOptions;
  }

  it('offers the commands the host service returns for the target device and bakes the pick', async () => {
    const commandChoices = vi.fn(async () => COMMANDS);
    const setOptions = renderEditor({ deviceToken: 'dev-1', commandChoices });

    // the choices load through the host service, then the picker appears
    const trigger = (await screen.findByText('Select a command')).closest('button')!;
    expect(commandChoices).toHaveBeenCalledWith('dev-1');
    // the withheld draft is NAMED, not silently omitted
    expect(screen.getByText('calibrate')).toBeTruthy();
    fireEvent.click(trigger);
    fireEvent.click(await screen.findByText('Set point (setpoint)'));

    expect(setOptions).toHaveBeenCalledWith({
      commandName: 'setpoint',
      commandLabel: 'Set point',
      parameterSchema: '[{"name":"target","type":"FLOAT"}]',
    });
  });

  it('asks for a target device first, and copes with a host that supplies no command lookup', async () => {
    renderEditor({ deviceToken: undefined });
    expect(await screen.findByText(/select a (target )?device/i)).toBeTruthy();
    cleanup();
    renderEditor({ deviceToken: 'dev-1', commandChoices: undefined });
    expect(await screen.findByText(/no (commands|published)/i)).toBeTruthy();
  });
});

describe('a trusted package supplying its own editor', () => {
  // The editor renders what it was handed, and exposes buttons that exercise the validated write.
  const CURVE_OPTIONS = {
    curve: { kind: 'enum', values: ['linear', 'quadratic'], doc: 'Valve curve.', label: 'Curve' },
    title: { kind: 'string', doc: 'Heading.', label: 'Heading' },
  } as const;

  function Valve({ options, setOptions, services, descriptor }: WidgetEditorProps<OptionsOf<typeof CURVE_OPTIONS>>) {
    // The wrong writes below are the point of the test, so they are made untyped on purpose.
    const loose = setOptions as (patch: Record<string, unknown>) => void;
    return (
      <div data-testid="valve-editor">
        <span data-testid="seen">
          {descriptor.id}|{String(options.curve)}|{services.deviceToken}|{Object.keys(services.slots).join(',')}
        </span>
        <button type="button" onClick={() => setOptions({ curve: 'quadratic' })}>
          quadratic
        </button>
        <button type="button" onClick={() => loose({ curve: 'zigzag' })}>
          bad value
        </button>
        <button type="button" onClick={() => loose({ nope: 1 })}>
          bad key
        </button>
      </div>
    );
  }

  const valveWidget = defineWidget({
    descriptor: {
      ...descriptorWith(CURVE_OPTIONS),
      id: 'acme/valve-curve' as const,
    },
    implementation: { kind: 'trusted-react', component: () => null, editor: Valve },
  });

  const widget: ResolvedWidgetInstance = {
    id: 'w1',
    type: 'acme/valve-curve' as ResolvedWidgetInstance['type'],
    widget: { version: '1.0.0', optionsVersion: 1 },
    layout: { base: { col: 0, colSpan: 4, row: 0, rowSpan: 4, z: 0 } },
    options: { curve: 'linear', legacy: 'kept' },
  } as ResolvedWidgetInstance;

  const slots: Record<string, SlotDefinition> = { tank: { type: 'device' }, plant: { type: 'anchor' } };

  function renderPanel() {
    const onChange = vi.fn();
    render(
      <WidgetConfigPanel
        widget={widget}
        registry={createWidgetRegistry([valveWidget])}
        datasource={{ kind: 'device', deviceToken: 'dev-7', measurements: [] }}
        slots={slots}
        onChange={onChange}
        onDatasource={vi.fn()}
        onClose={vi.fn()}
      />,
    );
    return onChange;
  }

  it('is rendered in place of the generic fields, handed its options and the host services explicitly', () => {
    renderPanel();
    expect(screen.getByTestId('valve-editor')).toBeTruthy();
    expect(screen.getByTestId('seen').textContent).toBe('acme/valve-curve|linear|dev-7|tank,plant');
    // The generic fields for the same options are NOT also drawn.
    expect(screen.queryByTestId('widget-option-field-curve')).toBeNull();
    expect(screen.queryByTestId('widget-option-field-title')).toBeNull();
  });

  it('is handed nothing but its declared contract: no hub, client, token or other host internal', () => {
    let seen: Record<string, unknown> | undefined;
    const Spy = (props: WidgetEditorProps) => {
      seen = props as unknown as Record<string, unknown>;
      return null;
    };
    const spyWidget = defineWidget({
      descriptor: valveWidget.descriptor,
      implementation: { kind: 'trusted-react', component: () => null, editor: Spy },
    });
    render(
      <WidgetConfigPanel
        widget={widget}
        registry={createWidgetRegistry([spyWidget])}
        datasource={{ kind: 'device', deviceToken: 'dev-7', measurements: [] }}
        slots={slots}
        onChange={vi.fn()}
        onDatasource={vi.fn()}
        onClose={vi.fn()}
      />,
    );
    expect(Object.keys(seen!).sort()).toEqual(['descriptor', 'options', 'services', 'setOptions']);
    // Each service is an explicit, named capability; anything else reaching an editor is a leak.
    const services = seen!.services as Record<string, unknown>;
    expect(Object.keys(services).filter((k) => !['slots', 'deviceToken', 'commandChoices'].includes(k))).toEqual([]);
    expect(typeof services.commandChoices).toBe('function');
  });

  it('contains an editor that throws while rendering to the editor, not the panel or the page', () => {
    const Broken = () => {
      throw new Error('editor bug');
    };
    const brokenWidget = defineWidget({
      descriptor: valveWidget.descriptor,
      implementation: { kind: 'trusted-react', component: () => null, editor: Broken },
    });
    const quiet = vi.spyOn(console, 'error').mockImplementation(() => {});
    try {
      render(
        <WidgetConfigPanel
          widget={widget}
          registry={createWidgetRegistry([brokenWidget])}
          datasource={undefined}
          slots={slots}
          onChange={vi.fn()}
          onDatasource={vi.fn()}
          onClose={vi.fn()}
        />,
      );
    } finally {
      quiet.mockRestore();
    }
    expect(screen.getByTestId('widget-editor-failed')).toBeTruthy();
    // The panel itself (its heading and close control) is still there.
    expect(screen.getByText('Probe')).toBeTruthy();
    expect(screen.getByRole('button', { name: /close/i })).toBeTruthy();
  });

  it('writes through a validated setOptions that keeps what it did not name', () => {
    const onChange = renderPanel();
    fireEvent.click(screen.getByRole('button', { name: 'quadratic' }));
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange.mock.calls[0][0].options).toEqual({ curve: 'quadratic', legacy: 'kept' });
  });

  it('refuses a value the schema cannot hold and a key it does not declare, writing nothing', () => {
    const onChange = renderPanel();
    // React reports an event handler's throw on the window; the write must not have happened.
    const quiet = vi.spyOn(console, 'error').mockImplementation(() => {});
    const swallow = (e: ErrorEvent) => e.preventDefault();
    window.addEventListener('error', swallow);
    try {
      fireEvent.click(screen.getByRole('button', { name: 'bad value' }));
      fireEvent.click(screen.getByRole('button', { name: 'bad key' }));
    } finally {
      window.removeEventListener('error', swallow);
      quiet.mockRestore();
    }
    expect(onChange).not.toHaveBeenCalled();
  });
});

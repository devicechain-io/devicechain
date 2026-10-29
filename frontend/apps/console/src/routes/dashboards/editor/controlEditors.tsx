// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The console's registered editors for the specialized authoring workflows a descriptor can
// name with an option's `control`: picking a device command (and baking its key, label and
// parameter schema together), picking a slot, and flagging an uncredited basemap.
//
// 🔴 THEY ARE EDITORS IN THE SAME SENSE A THIRD-PARTY PACKAGE'S ARE. Each is a component of the
// SDK's WidgetEditorProps -- the widget's options, a validated `setOptions`, and the host
// services passed explicitly (slots, the target device, a command lookup) -- and reaches
// nothing else of the panel. That is the contract a trusted package's own
// `implementation.editor` is written against, so the built-in workflows are proof the
// interface carries a real one, not a shape only a hypothetical editor fits.
//
// WHICH EDITOR A DESCRIPTOR GETS IS ASKED OF THE DESCRIPTOR: an option's declared `control` is
// looked up here, and nothing about a widget id or an option's NAME selects one. A control this
// registry does not hold (an older console meeting a newer package's control) simply has no
// entry, and its option renders as the generic field its kind implies.

import type { OptionSpec, WidgetDescriptor } from '@devicechain/dashboards';
import type { WidgetEditorProps } from '@devicechain/widget-sdk';
import type { ComponentType } from 'react';
import { useTranslation } from 'react-i18next';

import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { FormField } from '@/components/ui/form-field';
import { useQuery } from '@/lib/hooks/use-query';
import '@/i18n/lazy/dashboards';

// What an editor registered for a control receives: the SDK's editor props plus the option
// keys the descriptor declared under this control, in schema order.
export interface ControlEditorProps extends WidgetEditorProps {
  readonly optionKeys: readonly string[];
}

export interface HostControl {
  // The id of the one field that stands for every option declaring this control, so a test
  // (and the exhaustiveness gate) can find it.
  fieldId: string;
  // Whether this descriptor's declaration of the control is one the editor can serve. A
  // descriptor that names the control on keys the workflow cannot write gets the generic
  // fields instead, as a host that did not know the control would.
  applies?: (descriptor: WidgetDescriptor) => boolean;
  // The editor. Absent for a control that adds no field of its own (its options render as
  // ordinary fields and only a notice below them is host-specific).
  Editor?: ComponentType<ControlEditorProps>;
  // A cross-field notice shown below the fields, read from the options alone.
  Notice?: ComponentType<{ options: Readonly<Record<string, unknown>>; descriptor: WidgetDescriptor }>;
}

const own = (o: object, key: string) => Object.prototype.hasOwnProperty.call(o, key);

// The control registered under `name`, or undefined. An own-property read: a declared control
// of 'constructor' must not resolve to an inherited member.
export function hostControl(name: string | undefined): HostControl | undefined {
  return name !== undefined && own(HOST_CONTROLS, name) ? HOST_CONTROLS[name] : undefined;
}

// The editor-bearing control an option declares, when this descriptor's declaration is
// servable. Used by the field list to collapse every option under a control into one field.
export function editorControlOf(descriptor: WidgetDescriptor, spec: OptionSpec): string | undefined {
  const control = hostControl(spec.control);
  if (!control?.Editor || spec.control === undefined) return undefined;
  return control.applies && !control.applies(descriptor) ? undefined : spec.control;
}

function optionString(options: Readonly<Record<string, unknown>>, key: string): string {
  const value = options[key];
  return typeof value === 'string' ? value : '';
}

// ---- command-vocabulary --------------------------------------------------------

// The command workflow writes all three of these keys at once, so it is offered only to a
// descriptor that declares all three under the control. The names are the workflow's own
// vocabulary (what the command button reads), not a way of choosing the workflow.
const KEY_COMMAND_NAME = 'commandName';
const KEY_COMMAND_LABEL = 'commandLabel';
const KEY_PARAMETER_SCHEMA = 'parameterSchema';

function declaresCommandTriple(descriptor: WidgetDescriptor): boolean {
  return [KEY_COMMAND_NAME, KEY_COMMAND_LABEL, KEY_PARAMETER_SCHEMA].every(
    (key) => own(descriptor.options, key) && descriptor.options[key].control === 'command-vocabulary',
  );
}

// CommandEditor lets the author pick which command the button issues, from the target
// device's PUBLISHED command vocabulary (ADR-043 decision 3). Picking one bakes its key,
// label, and parameter schema into the widget's options -- so the widget renders its typed
// form at runtime with no device->profile resolution. Requires a target device to be chosen
// first (that's where the command list comes from).
//
// When the device's profile CONSTRAINS its vocabulary, the picker offers published commands
// only, because published is what the enqueue gate accepts. Baking a draft there would
// produce a button that looks correct in the editor, renders correctly on the dashboard, and
// fails only when an operator presses it. Draft-only commands are still NAMED below the
// picker rather than omitted: an author who just wrote a command definition and can't find
// it needs to be told it is unpublished, not left to conclude the editor is broken.
//
// When the profile does NOT constrain (no profile, never published, or no definitions --
// ADR-043 decision 4), the gate accepts any key, so the drafts ARE offerable and are offered.
// Restricting to published there would leave the whole unconstrained device class -- most
// devices, pre-GA -- with an empty picker and no way to author a command button at all, for
// a button that would have worked.
function CommandEditor({ options, setOptions, services }: ControlEditorProps) {
  const { t } = useTranslation('dashboards');
  const deviceToken = services.deviceToken;
  const lookup = services.commandChoices;
  const commandName = optionString(options, KEY_COMMAND_NAME);
  const { current: data, loading, error } = useQuery(
    async () => {
      if (!deviceToken || !lookup) return { selectable: [], draftOnly: [], constrained: false };
      return lookup(deviceToken);
    },
    [deviceToken, lookup],
  );
  const selectable = data?.selectable ?? [];
  const draftOnly = data?.draftOnly ?? [];
  const constrained = data?.constrained ?? false;

  const choices: ComboboxOption[] = selectable.map((def) => ({
    value: def.commandKey,
    label: def.name ? `${def.name} (${def.commandKey})` : def.commandKey,
  }));

  const select = (def: (typeof selectable)[number] | undefined) =>
    setOptions({
      [KEY_COMMAND_NAME]: def?.commandKey,
      [KEY_COMMAND_LABEL]: def?.name ?? def?.commandKey,
      [KEY_PARAMETER_SCHEMA]: def?.parameterSchema ?? undefined,
    });

  // A command baked from a previous target device may not exist on the current one (the
  // author repointed the device without re-picking), or may have been unpublished since.
  // Flag it -- non-destructively, since the datasource and options update through separate
  // handlers -- so the author re-picks rather than silently shipping a button that fails.
  const staleSelection =
    commandName !== '' && !loading && !error && !selectable.some((d) => d.commandKey === commandName);

  return (
    <div className="space-y-3 rounded-md border border-border p-3">
      <div className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        {t('widgetCommandHeading')}
      </div>
      {!deviceToken ? (
        <p className="text-xs text-muted-foreground">{t('widgetCommandSelectDevice')}</p>
      ) : loading ? (
        <p className="text-xs text-muted-foreground">{t('widgetCommandLoading')}</p>
      ) : error ? (
        <p className="text-xs text-muted-foreground">{t('widgetCommandLoadError', { error })}</p>
      ) : (
        <>
          {staleSelection && (
            <p className="text-xs text-destructive">{t('widgetCommandStale', { commandName })}</p>
          )}
          {selectable.length === 0 ? (
            <p className="text-xs text-muted-foreground">{t('widgetCommandNoneDefined')}</p>
          ) : (
            <FormField label={t('widgetCommandHeading')} description={t('widgetCommandFieldHint')}>
              <Combobox
                options={choices}
                value={commandName}
                onChange={(key) => select(selectable.find((d) => d.commandKey === key))}
                placeholder={t('widgetCommandSelectPlaceholder')}
              />
            </FormField>
          )}
          {!constrained && selectable.length > 0 && (
            <p className="text-xs text-muted-foreground">{t('widgetCommandUnpublishedHint')}</p>
          )}
          {draftOnly.length > 0 && (
            <div className="space-y-1 rounded-md bg-muted/50 p-2">
              <p className="text-xs text-muted-foreground">{t('widgetCommandDraftOnlyLabel')}</p>
              {/* Actual (untranslated) command names -- data, not prose. */}
              <p className="text-xs font-medium text-muted-foreground">{draftOnly.join(', ')}</p>
              <p className="text-xs text-muted-foreground">
                {t('widgetCommandDraftOnlyPublishHint', { count: draftOnly.length })}
              </p>
            </div>
          )}
        </>
      )}
    </div>
  );
}

// ---- slot-target ---------------------------------------------------------------

// SlotTargetEditor picks which slot a widget re-points when a viewer selects an entity in
// it. A scoped slot becomes a member picker (which device within the parent context); a
// root slot a context picker (which building/customer the dashboard shows). The target is
// stored in the widget's options; the runtime resolves the candidate set from the target
// slot's scope.
//
// Every widget whose descriptor declares the slot-target control gets it, under whatever
// option key it chose to store the target in.
function SlotTargetEditor({ options, setOptions, services, optionKeys }: ControlEditorProps) {
  const { t } = useTranslation('dashboards');
  const key = optionKeys[0];
  const slots = services.slots;
  const choices: ComboboxOption[] = Object.keys(slots).map((name) => {
    const slot = slots[name];
    const role = slot.scope
      ? t('widgetRoleMemberOf', { parent: slot.scope.parent })
      : slot.type === 'anchor'
        ? t('widgetRoleContext')
        : t('widgetRoleDevice');
    return { value: name, label: name, description: role };
  });

  return (
    <div className="space-y-3 rounded-md border border-border p-3">
      <div className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        {t('widgetSelectionTargetHeading')}
      </div>
      {choices.length === 0 ? (
        <p className="text-xs text-muted-foreground">{t('widgetSelectionTargetEmptyHint')}</p>
      ) : (
        <FormField label={t('widgetLabelTargetSlot')} description={t('widgetTargetSlotHint')}>
          <Combobox
            options={choices}
            value={optionString(options, key)}
            onChange={(v) => setOptions({ [key]: v || undefined })}
            placeholder={t('widgetPlaceholderSelectSlot')}
          />
        </FormField>
      )}
    </div>
  );
}

// ---- basemap -------------------------------------------------------------------

const KEY_TILE_URL = 'tileUrl';
const KEY_ATTRIBUTION = 'attribution';

// declaresTilePair answers whether a descriptor reads BOTH halves of the basemap override,
// each under the basemap control. An own-property test rather than `in`, so a key reaching
// here from data ('constructor' and friends) is not mistaken for a declared option.
function declaresTilePair(descriptor: WidgetDescriptor): boolean {
  const declares = (key: string) => own(descriptor.options, key) && descriptor.options[key].control === 'basemap';
  return declares(KEY_TILE_URL) && declares(KEY_ATTRIBUTION);
}

// 🔴 A widget tile source with no credit line is IGNORED at render -- the two halves are one
// value, and this tier never reaches the server's validation, so the renderer discards the
// pair rather than drawing a provider's tiles uncredited. Saying so is the whole point:
// without it the option silently does nothing and reads as broken, which is the complaint
// the basemap work started from. The geofence editor carries the same message.
//
// Stated limit: declaring the pair is a PROXY for rendering tiles, not proof of it; a widget
// that declared both and did not discard the unattributed pair at render would be told
// something untrue here. Nothing declares them today but the map, and the discard rule lives
// with the widget that does.
function BasemapNotice({
  options,
  descriptor,
}: {
  options: Readonly<Record<string, unknown>>;
  descriptor: WidgetDescriptor;
}) {
  const { t } = useTranslation('dashboards');
  if (!declaresTilePair(descriptor)) return null;
  if (optionString(options, KEY_TILE_URL).trim() === '') return null;
  if (optionString(options, KEY_ATTRIBUTION).trim() !== '') return null;
  return (
    <p className="text-destructive text-xs" data-testid="widget-tile-uncredited">
      {t('widgetTileUncredited')}
    </p>
  );
}

// ---- the registry ----------------------------------------------------------------

const HOST_CONTROLS: Readonly<Record<string, HostControl>> = {
  'command-vocabulary': {
    fieldId: 'command',
    applies: declaresCommandTriple,
    Editor: CommandEditor,
  },
  'slot-target': {
    fieldId: 'slot',
    Editor: SlotTargetEditor,
  },
  basemap: {
    fieldId: 'basemap',
    Notice: BasemapNotice,
  },
};

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The detection-rule authoring surface: a Form ⇄ Canvas ⇄ NL toggle over the three authoring
// doors (ADR-053 form/canvas; ADR-056 natural language). All produce the SAME DetectionRule draft
// through the same publish gate — the form is the floor (fast, linear), the canvas the ceiling
// (visual), and the NL door a drafting shortcut that lands the author back in the form to review.
// A canvas-authored rule (one carrying an AuthoringGraph) defaults to the canvas; everything else
// to the form. Switching mode re-initialises the chosen editor, so an unsaved edit in one mode
// does not carry into the other — an intentional, no-surprise boundary. The NL door is offered
// only when creating a new rule (there is nothing to "describe" when editing an existing one).

import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { SegmentedControl, type SegmentedOption } from '@/components/ui/segmented-control';
import { DetectionRuleForm } from './DetectionRuleForm';
import { parseDefinition, unmodelledRuleType } from './rule-model';
import { DetectionRuleNLDraft } from './DetectionRuleNLDraft';
import { CanvasEditor } from './canvas/CanvasEditor';
import { listMetricDefinitions, type DetectionRule } from '@/lib/api/device-management';
import { PRESET_IDS, BATTERY_HOLD, BATTERY_LOW_BELOW, HELD_HOLD, SILENT_TIMEOUT, buildPreset, type PresetId } from './rule-presets';
import type { RuleWarning } from '@/lib/api/event-processing';
import '@/i18n/lazy/deviceProfiles';

type Mode = 'form' | 'canvas' | 'nl';

export function DetectionRuleAuthoring({
  profileToken,
  entity,
  onDone,
}: {
  profileToken: string;
  entity?: DetectionRule;
  onDone: (message: string) => void;
}) {
  const { t } = useTranslation('deviceProfiles');
  const [mode, setMode] = useState<Mode>(entity?.authoringGraph ? 'canvas' : 'form');
  // A compiled draft handed over from the NL door, used to pre-fill the form for a NEW rule. It
  // is passed as `initialDefinition` (not `entity`), so the form stays on the create path.
  const [nlDraft, setNlDraft] = useState<string | undefined>();
  const [nlWarnings, setNlWarnings] = useState<RuleWarning[]>([]);
  const creating = entity == null;
  // A preset's definition, handed to the form exactly like the NL draft is: pre-fill, never save.
  // `presetNonce` remounts the form so picking a second preset (or the same one again) re-fills it.
  const [preset, setPreset] = useState<{ id: PresetId; definition: string; metricToConfirm: boolean } | null>(null);
  const [presetNonce, setPresetNonce] = useState(0);
  const [metrics, setMetrics] = useState<string[]>([]);
  useEffect(() => {
    if (!creating) return;
    let cancelled = false;
    listMetricDefinitions(profileToken)
      .then((ms) => {
        if (!cancelled) setMetrics(ms.map((m) => m.metricKey));
      })
      .catch(() => {
        /* no metric hints: the presets still open, with the metric left for the author to choose */
      });
    return () => {
      cancelled = true;
    };
  }, [creating, profileToken]);

  const pickPreset = (id: PresetId) => {
    const out = buildPreset(
      id,
      { metrics },
      { name: t(`preset${id[0].toUpperCase()}${id.slice(1)}Name`), description: t(`preset${id[0].toUpperCase()}${id.slice(1)}Description`) },
    );
    setPreset({ id, definition: out.definition, metricToConfirm: out.metricToConfirm });
    setNlDraft(undefined);
    setNlWarnings([]);
    setPresetNonce((n) => n + 1);
  };

  // Built above the return, not inline in the attribute: 'form'/'canvas'/'nl' are the
  // Mode discriminant rather than user text, and a literal inside a JSX attribute is
  // what the i18n lint rule is watching for. The labels beside them ARE display text
  // and stay translated. The "Describe" door only exists while creating.
  const modeOptions: SegmentedOption<Mode>[] = [
    { value: 'form', label: t('ruleModeForm') },
    { value: 'canvas', label: t('ruleModeCanvas') },
    ...(creating ? [{ value: 'nl' as const, label: t('ruleModeDescribe') }] : []),
  ];

  return (
    <div className="space-y-4">
      <SegmentedControl<Mode>
        ariaLabel={t('ruleModePicker')}
        value={mode}
        onValueChange={(m) => {
          // A draft the form refuses to open (unmodelled type / unreadable) is dropped when the
          // author leaves the form, so coming back is not stuck on a read-only view of it.
          if (m !== 'form' && nlDraft != null && (unmodelledRuleType(nlDraft) != null || parseDefinition(nlDraft) == null)) {
            setNlDraft(undefined);
          }
          setMode(m);
        }}
        size="md"
        options={modeOptions}
      />
      {mode === 'nl' ? (
        <DetectionRuleNLDraft
          profileToken={profileToken}
          onDrafted={(definition, warnings) => {
            // Land the compiled draft in the form for the human to review and save.
            setNlDraft(definition);
            setNlWarnings(warnings);
            // eslint-disable-next-line i18next/no-literal-string -- 'form' is the Mode discriminant.
            setMode('form');
          }}
        />
      ) : mode === 'form' ? (
        <>
          {creating && (
            <PresetPicker
              active={preset?.id ?? null}
              metricToConfirm={preset?.metricToConfirm ?? false}
              onPick={pickPreset}
            />
          )}
          <DetectionRuleForm
            key={presetNonce}
            profileToken={profileToken}
            entity={entity}
            initialDefinition={entity ? undefined : (preset?.definition ?? nlDraft)}
            initialWarnings={entity || preset ? undefined : nlWarnings}
            onDone={onDone}
          />
        </>
      ) : (
        <CanvasEditor profileToken={profileToken} entity={entity} onDone={onDone} />
      )}
    </div>
  );
}

// "Start from a preset": the three alerts operators ask for first, each a pre-filled form. Picking
// one REPLACES what is in the form below (it re-fills it), so it sits above the form and only
// while creating — an existing rule is never overwritten by a preset.
function PresetPicker({
  active,
  metricToConfirm,
  onPick,
}: {
  active: PresetId | null;
  metricToConfirm: boolean;
  onPick: (id: PresetId) => void;
}) {
  const { t } = useTranslation('deviceProfiles');
  // The documented defaults, composed into the help text from the same constants the preset uses.
  const values: Record<PresetId, Record<string, string | number>> = {
    deviceSilent: { timeout: SILENT_TIMEOUT },
    heldThreshold: { hold: HELD_HOLD },
    lowBattery: { below: BATTERY_LOW_BELOW, hold: BATTERY_HOLD },
  };
  return (
    <div className="space-y-2 rounded-md border p-3" data-shot="rule-presets">
      <div>
        <span className="text-sm font-semibold">{t('presetPickerTitle')}</span>
        <p className="text-xs text-muted-foreground">{t('presetPickerHelp')}</p>
      </div>
      <div className="flex flex-wrap gap-2">
        {PRESET_IDS.map((id) => (
          <Button
            key={id}
            size="sm"
            variant={active === id ? 'default' : 'outline'}
            data-shot="rule-preset"
            data-shot-preset={id}
            onClick={() => onPick(id)}
          >
            {t(`preset${id[0].toUpperCase()}${id.slice(1)}Name`)}
          </Button>
        ))}
      </div>
      {active != null && (
        <div className="space-y-1" data-testid="preset-help">
          <p className="text-xs text-muted-foreground">
            {t(`preset${active[0].toUpperCase()}${active.slice(1)}Help`, values[active])}
          </p>
          {metricToConfirm && <p className="text-xs text-amber-600 dark:text-amber-500">{t('presetConfirmMetric')}</p>}
        </div>
      )}
    </div>
  );
}

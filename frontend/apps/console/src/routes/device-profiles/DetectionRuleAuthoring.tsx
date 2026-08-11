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

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { SegmentedControl, type SegmentedOption } from '@/components/ui/segmented-control';
import { DetectionRuleForm } from './DetectionRuleForm';
import { DetectionRuleNLDraft } from './DetectionRuleNLDraft';
import { CanvasEditor } from './canvas/CanvasEditor';
import type { DetectionRule } from '@/lib/api/device-management';

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
  const creating = entity == null;

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
        onValueChange={setMode}
        size="md"
        options={modeOptions}
      />
      {mode === 'nl' ? (
        <DetectionRuleNLDraft
          profileToken={profileToken}
          onDrafted={(definition) => {
            // Land the compiled draft in the form for the human to review and save.
            setNlDraft(definition);
            // eslint-disable-next-line i18next/no-literal-string -- 'form' is the Mode discriminant.
            setMode('form');
          }}
        />
      ) : mode === 'form' ? (
        <DetectionRuleForm
          profileToken={profileToken}
          entity={entity}
          initialDefinition={entity ? undefined : nlDraft}
          onDone={onDone}
        />
      ) : (
        <CanvasEditor profileToken={profileToken} entity={entity} onDone={onDone} />
      )}
    </div>
  );
}

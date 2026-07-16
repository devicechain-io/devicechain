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
import { cn } from '@/lib/utils';
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
  const [mode, setMode] = useState<Mode>(entity?.authoringGraph ? 'canvas' : 'form');
  // A compiled draft handed over from the NL door, used to pre-fill the form for a NEW rule. It
  // is passed as `initialDefinition` (not `entity`), so the form stays on the create path.
  const [nlDraft, setNlDraft] = useState<string | undefined>();
  const creating = entity == null;

  return (
    <div className="space-y-4">
      <div className="inline-flex rounded-md border p-0.5 text-sm">
        <ModeButton active={mode === 'form'} onClick={() => setMode('form')}>
          Form
        </ModeButton>
        <ModeButton active={mode === 'canvas'} onClick={() => setMode('canvas')}>
          Canvas
        </ModeButton>
        {creating && (
          <ModeButton active={mode === 'nl'} onClick={() => setMode('nl')}>
            Describe
          </ModeButton>
        )}
      </div>
      {mode === 'nl' ? (
        <DetectionRuleNLDraft
          profileToken={profileToken}
          onDrafted={(definition) => {
            // Land the compiled draft in the form for the human to review and save.
            setNlDraft(definition);
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

function ModeButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'rounded px-3 py-1 transition-colors',
        active ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:text-foreground',
      )}
    >
      {children}
    </button>
  );
}

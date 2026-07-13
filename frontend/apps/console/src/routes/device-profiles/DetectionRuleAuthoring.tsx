// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The detection-rule authoring surface: a Form ⇄ Canvas toggle over the two authoring modes
// (ADR-053). Both produce the SAME DetectionRule draft through the same publish gate — the
// form is the floor (fast, linear), the canvas the ceiling (visual, ADR-053). A canvas-authored
// rule (one carrying an AuthoringGraph) defaults to the canvas; everything else to the form.
// Switching mode re-initialises the chosen editor from the stored rule, so an unsaved edit in
// one mode does not carry into the other — an intentional, no-surprise boundary.

import { useState } from 'react';
import { cn } from '@/lib/utils';
import { DetectionRuleForm } from './DetectionRuleForm';
import { CanvasEditor } from './canvas/CanvasEditor';
import type { DetectionRule } from '@/lib/api/device-management';

type Mode = 'form' | 'canvas';

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

  return (
    <div className="space-y-4">
      <div className="inline-flex rounded-md border p-0.5 text-sm">
        <ModeButton active={mode === 'form'} onClick={() => setMode('form')}>
          Form
        </ModeButton>
        <ModeButton active={mode === 'canvas'} onClick={() => setMode('canvas')}>
          Canvas
        </ModeButton>
      </div>
      {mode === 'form' ? (
        <DetectionRuleForm profileToken={profileToken} entity={entity} onDone={onDone} />
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

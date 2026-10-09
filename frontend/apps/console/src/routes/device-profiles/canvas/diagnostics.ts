// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { TFunction } from 'i18next';
import type { CanvasCompileResult } from '@/lib/api/event-processing';
import { ruleWarningText } from '@/lib/ruleWarnings';

// paintDiagnostics splits the compiler's node-anchored diagnostics into what each node shows: an
// ERROR (red, blocks saving) and, separately, a WARNING (amber, advisory — the graph compiled). A
// warning with a code is localised from it; one without (the lowering's own) shows its message.
export function paintDiagnostics(
  diags: CanvasCompileResult['diagnostics'],
  t: TFunction,
): Map<string, { diagnostic?: string; warning?: string }> {
  const byNode = new Map<string, { diagnostic?: string; warning?: string }>();
  for (const d of diags) {
    if (!d.nodeId) continue;
    const cur = byNode.get(d.nodeId) ?? {};
    if (d.severity === 'warning') {
      const text = ruleWarningText(t, d);
      cur.warning = cur.warning ? `${cur.warning} ${text}` : text;
    } else {
      cur.diagnostic = cur.diagnostic ?? d.message;
    }
    byNode.set(d.nodeId, cur);
  }
  return byNode;
}

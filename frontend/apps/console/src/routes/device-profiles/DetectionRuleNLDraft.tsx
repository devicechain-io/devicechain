// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The natural-language authoring door (ADR-056 slice 1) — the third way to author a DETECT
// rule, alongside the form and the canvas. The author describes the rule in plain language; the
// server asks the active inference provider to PROPOSE a candidate and runs it through the SAME
// rules.Compile firewall the other two doors use ("AI proposes, the compiler disposes" — the AI
// never sits in the replay-correct path). A compiling draft is handed to the form for the human
// to review and save through the normal create door; this panel persists nothing.
//
// External model routing is a per-tenant, fail-closed opt-in: with no active/consented provider
// the server returns `unavailable` (not an error), and this panel says so rather than pretending
// the feature is broken. A non-compiling draft comes back with the compiler's diagnostics + the
// model's raw attempt, so the author can refine the description and try again.

import { useEffect, useRef, useState } from 'react';
import { Button } from '@/components/ui/button';
import { ErrorBanner } from '@/components/ui/error-banner';
import { Textarea, errMessage } from '@/routes/common';
import { listMetricDefinitions } from '@/lib/api/device-management';
import {
  draftDetectionRuleFromText,
  type DraftRuleResult,
  type MetricHintInput,
} from '@/lib/api/event-processing';

export function DetectionRuleNLDraft({
  profileToken,
  onDrafted,
}: {
  profileToken: string;
  // Called with the compiled rules.Rule JSON once a draft compiles — the parent hands it to the
  // form (pre-filled, still a NEW rule) for the human to review and save.
  onDrafted: (definition: string) => void;
}) {
  const [text, setText] = useState('');
  const [metrics, setMetrics] = useState<MetricHintInput[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // The last non-ok outcome (unavailable, or a non-compiling draft with diagnostics). An ok
  // outcome is not held here — the parent switches to the form on the same tick.
  const [result, setResult] = useState<DraftRuleResult | null>(null);
  // The draft mutation is slow by design (a bounded compile/repair loop). If the author switches
  // away (to the canvas/form) while it is in flight, this panel unmounts; the resolving call must
  // NOT then fire onDrafted — that would hijack the mode and could discard unsaved canvas work.
  const mountedRef = useRef(true);
  useEffect(() => () => {
    mountedRef.current = false;
  }, []);

  // Load the profile's metric vocabulary so the model references REAL metric keys (the same list
  // the Metrics tab shows). Best-effort: if it fails the model just works from the description.
  useEffect(() => {
    let live = true;
    listMetricDefinitions(profileToken)
      .then((defs) => {
        if (!live) return;
        setMetrics(
          defs
            .filter((d) => d.metricKey)
            .map((d) => ({
              key: d.metricKey,
              dataType: d.dataType ?? undefined,
              unit: d.unit ?? undefined,
              description: d.description ?? d.name ?? undefined,
            })),
        );
      })
      .catch(() => {
        /* advisory context only — a failure here just means a leaner prompt. */
      });
    return () => {
      live = false;
    };
  }, [profileToken]);

  async function submit() {
    const description = text.trim();
    if (!description || busy) return;
    setBusy(true);
    setError(null);
    setResult(null);
    try {
      const res = await draftDetectionRuleFromText({ text: description, profileToken, metrics });
      // The author may have navigated away while this was in flight — if so, drop the result
      // rather than yanking them out of whatever door they switched to.
      if (!mountedRef.current) return;
      if (res.ok) {
        if (res.definition) {
          // The compiler accepted it — hand the compiled draft to the form to review + save.
          onDrafted(res.definition);
          return;
        }
        // ok with no definition would be a server contract break; surface it rather than
        // silently flipping the button back to idle with nothing shown.
        setError('The draft compiled but returned no definition. Please try again.');
        return;
      }
      setResult(res);
    } catch (e) {
      if (mountedRef.current) setError(errMessage(e));
    } finally {
      if (mountedRef.current) setBusy(false);
    }
  }

  const unavailable = result?.unavailable === true;
  const rejected = result != null && !result.unavailable && !result.ok;

  return (
    <div className="space-y-4">
      <div className="space-y-1">
        <p id="nl-draft-help" className="text-sm text-muted-foreground">
          Describe the rule in plain language. AI drafts it and the compiler checks it; you review and
          save the result through the form.
        </p>
      </div>

      <Textarea
        value={text}
        onChange={(e) => setText(e.target.value)}
        rows={4}
        aria-label="Rule description"
        aria-describedby="nl-draft-help"
        placeholder="e.g. Raise a major alarm when the case temperature stays above 80°C for 5 minutes"
        onKeyDown={(e) => {
          // ⌘/Ctrl-Enter drafts, matching the app's other free-text submit affordances.
          if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
            e.preventDefault();
            void submit();
          }
        }}
      />

      <div className="flex items-center gap-3">
        <Button type="button" onClick={() => void submit()} disabled={busy || !text.trim()}>
          {busy ? 'Drafting…' : 'Draft rule'}
        </Button>
        <span className="text-xs text-muted-foreground">The AI proposes; the compiler decides. Nothing is saved until you do.</span>
      </div>

      {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}

      {unavailable && (
        <div className="rounded-md border border-amber-500/40 bg-amber-500/10 px-4 py-3 text-sm text-amber-700 dark:text-amber-400">
          {result?.unavailableReason ??
            'Natural-language drafting is not available — no inference provider is enabled for this tenant.'}
        </div>
      )}

      {rejected && (
        <div className="space-y-3">
          <div className="space-y-2">
            <p className="text-sm text-muted-foreground">
              The draft didn’t compile after {result?.attempts ?? 0} attempt
              {result?.attempts === 1 ? '' : 's'}. Refine your description and try again.
            </p>
            <ul className="space-y-1">
              {result?.diagnostics.map((d, i) => (
                <li
                  key={i}
                  className="rounded-md border border-destructive/50 bg-destructive/10 px-3 py-2 text-sm text-destructive"
                >
                  {d.field ? <span className="font-medium">{d.field}: </span> : null}
                  {d.message}
                </li>
              ))}
            </ul>
          </div>
          {result?.rawCandidate && (
            <details className="text-xs text-muted-foreground">
              <summary className="cursor-pointer select-none">What the AI tried</summary>
              <pre className="mt-2 max-h-64 overflow-auto rounded-md border bg-muted/40 p-3 text-xs">
                {result.rawCandidate}
              </pre>
            </details>
          )}
          {result?.provider && (
            <p className="text-xs text-muted-foreground">
              via {result.model || 'model'} ({result.provider})
            </p>
          )}
        </div>
      )}
    </div>
  );
}

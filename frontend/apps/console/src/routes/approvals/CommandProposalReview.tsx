// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { CommandAgentProposal } from '@/lib/api/command-proposals';
import { Badge } from '@/components/ui/badge';
import { CopyToken } from '@/components/ui/copy-token';
import { ErrorState } from '@/components/ui/error-state';
import { SectionPanel } from '@/components/ui/section-panel';
import { formatTime } from '@/lib/utils';
import { revealInvisible } from './invisible';

interface CommandSnapshot {
  deviceToken: string;
  command: string;
  incarnation: string;
  definition: string;
  cutoff: string;
  payload: string | null;
}
const tokens = (raw: string) => [...raw.matchAll(/"(?:\\.|[^"\\])*"|[{}\[\],:]|[^\s{}\[\],:]+/g)];

// Read the original JSON lexemes: JSON.parse/stringify would round large integers and
// erase decimal precision from the exact arguments the displayed digest approves.
function member(raw: string, key: string): string | null {
  const parts = tokens(raw);
  let depth = 0;
  for (let i = 0; i < parts.length; i++) {
    const value = parts[i][0];
    if (depth === 1 && value.startsWith('"') && parts[i + 1]?.[0] === ':' && JSON.parse(value) === key) {
      const first = i + 2;
      let last = first;
      if (parts[first]?.[0] === '{' || parts[first]?.[0] === '[') {
        let nested = 0;
        for (; last < parts.length; last++) {
          const v = parts[last][0];
          if (v === '{' || v === '[') nested++;
          if (v === '}' || v === ']') nested--;
          if (nested === 0) break;
        }
      }
      return raw.slice(parts[first].index, parts[last].index! + parts[last][0].length);
    }
    if (value === '{' || value === '[') depth++;
    if (value === '}' || value === ']') depth--;
  }
  return null;
}
function formatJSON(raw: string): string {
  const parts = tokens(raw).map(part => part[0]);
  let depth = 0;
  let out = '';
  const newline = () => '\n' + '  '.repeat(depth);
  parts.forEach((value, i) => {
    if (value === '{' || value === '[') {
      out += value; depth++;
      if (parts[i + 1] !== '}' && parts[i + 1] !== ']') out += newline();
    } else if (value === '}' || value === ']') {
      depth--;
      if (parts[i - 1] !== '{' && parts[i - 1] !== '[') out += newline();
      out += value;
    } else if (value === ',') out += ',' + newline();
    else if (value === ':') out += ': ';
    else out += value;
  });
  return out;
}
export function commandSnapshot(proposal: CommandAgentProposal): CommandSnapshot | null {
  try {
    const stored = JSON.parse(proposal.payload) as { v: unknown; intent: { deviceToken: unknown; command: unknown; payloadPresent: unknown }; effective: { incarnation: unknown; definition: unknown; impact: unknown; cutoff: unknown } };
    const { intent, effective } = stored;
    if (stored.v !== 1 || intent.deviceToken !== proposal.deviceToken || intent.command !== proposal.command ||
      typeof intent.payloadPresent !== 'boolean' || typeof effective.incarnation !== 'string' || !/^\d+$/.test(effective.incarnation) ||
      typeof effective.definition !== 'string' || !effective.definition || effective.impact !== proposal.impact ||
      effective.cutoff !== proposal.commandCutoff || !Number.isFinite(Date.parse(proposal.commandCutoff))) return null;
    const intentJSON = member(proposal.payload, 'intent');
    const payloadJSON = intentJSON === null ? null : member(intentJSON, 'payload');
    if (intent.payloadPresent && payloadJSON === null) return null;
    return { deviceToken: proposal.deviceToken, command: proposal.command, incarnation: effective.incarnation,
      definition: effective.definition, cutoff: proposal.commandCutoff, payload: intent.payloadPresent ? formatJSON(payloadJSON!) : null };
  } catch { return null; }
}
function ApprovalCountdown({ expiresAt }: { expiresAt: string }) {
  const { t } = useTranslation('approvals');
  const [now, setNow] = useState(Date.now);
  useEffect(() => { const timer = setInterval(() => setNow(Date.now()), 1000); return () => clearInterval(timer); }, []);
  const seconds = Math.max(0, Math.ceil((Date.parse(expiresAt) - now) / 1000));
  return <SectionPanel title={t('approvalCountdown')}>
    <time dateTime={expiresAt}>{formatTime(expiresAt)}</time>
    <p>{t(seconds > 0 ? 'approvalRemaining' : 'approvalExpired', { seconds })}</p>
    <p className="mt-3">{t('commandDeliveryNotice')}</p>
  </SectionPanel>;
}
// Revealed shows untrusted text with every invisible format character made visible, and a
// warning badge when there was any (see revealInvisible).
function Revealed({ value, pre, paragraph, className }: { value: string; pre?: boolean; paragraph?: boolean; className?: string }) {
  const { t } = useTranslation('approvals');
  const shown = revealInvisible(value);
  const text = pre ? <pre className={className}>{shown.text}</pre>
    : paragraph ? <p className={className}>{shown.text}</p> : <span className={className}>{shown.text}</span>;
  return <>{shown.found && <Badge variant="warning">{t('invisibleCharacters')}</Badge>}{text}</>;
}
export function CommandProposalReview({ proposal, snapshot }: { proposal: CommandAgentProposal; snapshot: CommandSnapshot | null }) {
  const { t } = useTranslation('approvals');
  return <>
    {snapshot ? <SectionPanel title={t('action')}><dl className="space-y-3">
      <dt>{t('device')}</dt><dd className="font-mono break-all">{snapshot.deviceToken}</dd>
      <dt>{t('incarnation')}</dt><dd>{snapshot.incarnation}</dd>
      <dt>{t('command')}</dt><dd className="whitespace-pre-wrap break-all"><Revealed value={snapshot.command} /></dd>
      <dt>{t('commandPayload')}</dt><dd>{snapshot.payload === null ? <p>{t('noCommandPayload')}</p> : <Revealed pre className="whitespace-pre-wrap break-all" value={snapshot.payload} />}</dd>
      <dt>{t('impact')}</dt><dd><Badge variant={proposal.impact === 'DESTRUCTIVE' ? 'destructive' : proposal.impact === 'DISRUPTIVE' ? 'warning' : 'outline'}>{proposal.impact}</Badge></dd>
      <dt>{t('definition')}</dt><dd className="font-mono break-all">{snapshot.definition}</dd>
      <dt>{t('commandCutoff')}</dt><dd><time dateTime={snapshot.cutoff}>{snapshot.cutoff}</time></dd>
      <dt>{t('digest')}</dt><dd><p className="font-mono break-all">{proposal.digest}</p><CopyToken value={proposal.digest} /></dd>
    </dl></SectionPanel> : <ErrorState description={t('snapshotUnavailable')} />}
    <ApprovalCountdown expiresAt={proposal.expiresAt} />
    <SectionPanel title={t('agentRationale')}><Revealed paragraph className="whitespace-pre-wrap break-words" value={proposal.rationale ?? ''} /></SectionPanel>
  </>;
}

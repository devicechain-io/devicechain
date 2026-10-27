// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Copy, Download } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import { useToast } from '@/components/ui/toast';

// The recovery codes, shown ONCE. The server keeps only a digest of each, so a set that is
// closed unread cannot be shown again (regenerating replaces it). The person has to say they
// have kept the codes before the screen lets go of them.
//
// 🔴 THE CODES LIVE IN THE PARENT'S STATE AND NOWHERE ELSE: not in a query cache, storage, the
// address or a log. Acknowledging hands control back, the parent drops the array, and this
// component unmounts, which takes the codes out of the DOM. Copy and download leave the page
// only by the person's own action (the clipboard, a file they save).
export function RecoveryCodesPanel({ codes, onDone }: { codes: string[]; onDone: () => void }) {
  const { t } = useTranslation('security');
  const { toast } = useToast();
  const [kept, setKept] = useState(false);

  const text = codes.join('\n');

  const copy = async () => {
    try {
      // Clipboard is undefined on insecure origins; say so rather than throw.
      if (!navigator.clipboard) throw new Error('clipboard unavailable');
      await navigator.clipboard.writeText(text);
      toast(t('codesCopied'));
    } catch {
      toast(t('codesCopyFailed'), 'error');
    }
  };

  const download = () => {
    const url = URL.createObjectURL(new Blob([`${text}\n`], { type: 'text/plain' }));
    const a = document.createElement('a');
    a.href = url;
    a.download = 'devicechain-recovery-codes.txt';
    document.body.appendChild(a);
    a.click();
    a.remove();
    // Deferred, as lib/csv.ts does: some browsers start the download after click() returns.
    setTimeout(() => URL.revokeObjectURL(url), 0);
  };

  return (
    <div className="space-y-4" data-shot="security-recovery-codes">
      <p className="text-sm text-muted-foreground">{t('codesIntro')}</p>
      <ul
        aria-label={t('codesListLabel')}
        className="grid grid-cols-1 gap-2 rounded-md border border-border bg-muted/40 p-4 font-mono text-sm sm:grid-cols-2"
      >
        {codes.map((c) => (
          <li key={c} data-shot-pii>
            {c}
          </li>
        ))}
      </ul>
      <div className="flex flex-wrap gap-2">
        <Button type="button" variant="outline" size="sm" onClick={() => void copy()}>
          <Copy size={14} /> {t('codesCopy')}
        </Button>
        <Button type="button" variant="outline" size="sm" onClick={download}>
          <Download size={14} /> {t('codesDownload')}
        </Button>
      </div>
      <label className="flex items-start gap-2 text-sm text-foreground">
        <Checkbox
          checked={kept}
          onCheckedChange={(v) => setKept(v === true)}
          aria-label={t('codesAcknowledge')}
          className="mt-0.5"
        />
        <span>{t('codesAcknowledge')}</span>
      </label>
      <Button type="button" disabled={!kept} onClick={onDone}>
        {t('codesContinue')}
      </Button>
    </div>
  );
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// CopyToken renders an entity's stable token as a compact, monospaced, click-to-copy
// control. It is the standard title-line adornment on detail pages (PageShell's
// `titleAdornment` slot): a token is the id everything else references, so it belongs
// beside the human name and must be trivially copyable.

import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Copy, Check } from 'lucide-react';
import { useToast } from '@/components/ui/toast';
import { cn } from '@/lib/utils';

export function CopyToken({ value, className }: { value: string; className?: string }) {
  const { t } = useTranslation('common');
  const { toast } = useToast();
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);

  // Clear the pending "copied" reset if the component unmounts mid-flash.
  useEffect(() => () => clearTimeout(timer.current), []);

  const copy = async () => {
    try {
      // clipboard is undefined on insecure origins; fail with a toast rather than throw.
      if (!navigator.clipboard) throw new Error('clipboard unavailable');
      await navigator.clipboard.writeText(value);
      setCopied(true);
      clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(false), 1500);
    } catch {
      toast(t('copyFailed'), 'error');
    }
  };

  return (
    <button
      type="button"
      onClick={copy}
      title={t('copyTitle', { value })}
      aria-label={t('copyAria', { value })}
      className={cn(
        'inline-flex min-w-0 max-w-[18rem] items-center gap-1.5 rounded-md border border-border bg-muted/40 px-2 py-0.5',
        'font-mono text-xs text-muted-foreground transition-colors hover:text-foreground',
        'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring',
        className,
      )}
    >
      <span className="min-w-0 truncate">{value}</span>
      {copied ? (
        <Check size={12} className="shrink-0 text-emerald-500" aria-hidden />
      ) : (
        <Copy size={12} className="shrink-0" aria-hidden />
      )}
    </button>
  );
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useRef, useState } from 'react';
import { Download, X } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { useToast } from '@/components/ui/toast';
import { errMessage } from '@/routes/common';
import {
  EXPORT_ROW_CAP,
  collectPages,
  downloadCsv,
  exportFilename,
  toCsv,
  type CsvColumn,
  type PageResult,
} from '@/lib/csv';

interface ExportCsvButtonProps<T> {
  /** File name stem; the UTC date and `.csv` are appended. */
  fileStem: string;
  /** Columns, in export order, with already-localized headers. */
  columns: CsvColumn<T>[];
  /**
   * Fetches one page of the list EXACTLY as the page's own query would, for the filters
   * currently applied. It must go through the same API function the list uses, so the
   * export can never reach rows (or fields) the caller's own view could not.
   */
  fetchPage: (pageNumber: number, pageSize: number) => Promise<PageResult<T>>;
  /** Re-created on a filter change by the caller; any in-flight export is cancelled then. */
  scopeKey?: string;
  disabled?: boolean;
}

// Exports the list behind the page as CSV: every row matching the current filter (not just
// the visible page), up to a hard cap, with progress and a cancel. A truncated result is
// said so, in a message that stays on screen until the next export.
export function ExportCsvButton<T>({
  fileStem,
  columns,
  fetchPage,
  scopeKey,
  disabled,
}: ExportCsvButtonProps<T>) {
  const { t } = useTranslation('common');
  const { toast } = useToast();
  const [progress, setProgress] = useState<number | null>(null);
  const [notice, setNotice] = useState<{ text: string; tone: 'info' | 'error' } | null>(null);
  const ctl = useRef<AbortController | null>(null);
  const running = progress !== null;

  // A filter change or unmount abandons the export: the file would describe a list the
  // operator is no longer looking at.
  useEffect(() => {
    return () => ctl.current?.abort();
  }, [scopeKey]);

  const start = async () => {
    if (ctl.current && !ctl.current.signal.aborted) return;
    const controller = new AbortController();
    ctl.current = controller;
    setNotice(null);
    setProgress(0);
    try {
      const { rows, truncated } = await collectPages(fetchPage, {
        signal: controller.signal,
        onProgress: setProgress,
      });
      if (rows.length === 0) {
        setNotice({ text: t('exportEmpty'), tone: 'info' });
        return;
      }
      downloadCsv(exportFilename(fileStem), toCsv(columns, rows));
      if (truncated) {
        const text = t('exportTruncated', { count: rows.length, cap: EXPORT_ROW_CAP });
        setNotice({ text, tone: 'info' });
        toast(text, 'info');
      } else {
        toast(t('exportDone', { count: rows.length }));
      }
    } catch (err) {
      if (err instanceof DOMException && err.name === 'AbortError') {
        if (ctl.current === controller) setNotice({ text: t('exportCancelled'), tone: 'info' });
      } else {
        const text = t('exportFailed', { error: errMessage(err) });
        setNotice({ text, tone: 'error' });
        toast(text, 'error');
      }
    } finally {
      if (ctl.current === controller) {
        ctl.current = null;
        setProgress(null);
      }
    }
  };

  return (
    <div className="flex items-center gap-2">
      {notice && (
        <span
          role="status"
          className={
            notice.tone === 'error' ? 'text-xs text-destructive' : 'text-xs text-muted-foreground'
          }
        >
          {notice.text}
        </span>
      )}
      {running ? (
        <>
          <span role="status" className="text-xs text-muted-foreground tabular-nums">
            {t('exportProgress', { count: progress })}
          </span>
          <Button variant="outline" onClick={() => ctl.current?.abort()}>
            <X size={16} /> {t('exportCancel')}
          </Button>
        </>
      ) : (
        <Button variant="outline" onClick={start} disabled={disabled}>
          <Download size={16} /> {t('exportCsv')}
        </Button>
      )}
    </div>
  );
}

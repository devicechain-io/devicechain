// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A searchable single-select whose options come from the SERVER as the operator types.
//
// Combobox filters a list it already holds, so a picker built on it has to fetch the whole
// registry first - and the largest page the API serves is 1,000, so past that the tail is
// silently missing and unreachable. This one asks for a small page matching what was typed
// (debounced) and says so when there are more matches than it shows, so a long registry is
// reached by typing rather than by scrolling a truncated list.

import { useEffect, useId, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Check, ChevronsUpDown, Loader2, X } from 'lucide-react';
import { cn } from '@/lib/utils';
import { FIELD_POPOVER_WIDTH } from './popover-width';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { optionLabel, type ComboboxOption } from '@/components/ui/combobox';

/** One page of matches. `total` is the full match count when the server knows it. */
export interface AsyncComboboxPage {
  options: ComboboxOption[];
  total?: number;
}

export const ASYNC_COMBOBOX_PAGE_SIZE = 25;
export const ASYNC_COMBOBOX_DEBOUNCE_MS = 250;

interface AsyncComboboxProps {
  value: string;
  onChange: (value: string) => void;
  /** Fetch the options matching `query` (blank on open: the first page of everything). */
  search: (query: string) => Promise<AsyncComboboxPage>;
  /**
   * Label a value that the current page of results does not contain - the selection made
   * earlier, or one that arrived from a URL or a saved record. Without it the trigger shows
   * the raw value.
   */
  resolve?: (value: string) => Promise<ComboboxOption | null>;
  id?: string;
  placeholder?: string;
  searchPlaceholder?: string;
  emptyMessage?: string;
  disabled?: boolean;
  allowClear?: boolean;
  /** Accessible name for a combobox with no visible label (see Combobox.ariaLabel). */
  ariaLabel?: string;
  className?: string;
  /** Re-run the search when any of these change (a parent filter, say). */
  deps?: unknown[];
}

const triggerClasses =
  'flex h-10 w-full items-center justify-between gap-2 rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50';

export function AsyncCombobox({
  value,
  onChange,
  search,
  resolve,
  id,
  placeholder,
  searchPlaceholder,
  emptyMessage,
  disabled,
  allowClear = true,
  ariaLabel,
  className,
  deps = [],
}: AsyncComboboxProps) {
  const { t } = useTranslation('common');
  const generatedId = useId();
  const triggerId = id ?? `async-combobox-${generatedId}`;
  const labelId = `${triggerId}-label`;
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [active, setActive] = useState(0);
  const [page, setPage] = useState<AsyncComboboxPage>({ options: [] });
  const [loading, setLoading] = useState(false);
  const [failed, setFailed] = useState(false);
  // The option the trigger names. Remembered across searches: choosing it replaces the
  // results, so the trigger cannot rely on finding it in them.
  const [chosen, setChosen] = useState<ComboboxOption | null>(null);
  const activeRef = useRef<HTMLButtonElement>(null);
  // Only the newest request may land; a slow earlier answer must not overwrite it.
  const seq = useRef(0);
  const searchRef = useRef(search);
  searchRef.current = search;
  const resolveRef = useRef(resolve);
  resolveRef.current = resolve;

  useEffect(() => {
    if (!open) return;
    const mine = ++seq.current;
    setLoading(true);
    setFailed(false);
    // The first page (blank query) is immediate; typing waits for a pause.
    const wait = query === '' ? 0 : ASYNC_COMBOBOX_DEBOUNCE_MS;
    const timer = setTimeout(() => {
      searchRef
        .current(query)
        .then((p) => {
          if (seq.current !== mine) return;
          setPage(p);
          setActive(0);
          setLoading(false);
        })
        .catch(() => {
          if (seq.current !== mine) return;
          setPage({ options: [] });
          setFailed(true);
          setLoading(false);
        });
    }, wait);
    return () => clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, query, ...deps]);

  useEffect(() => {
    if (open) {
      setQuery('');
      setActive(0);
    }
  }, [open]);

  // Name the current value: from what we chose, from the page we hold, or by asking.
  useEffect(() => {
    if (!value) {
      setChosen(null);
      return;
    }
    if (chosen?.value === value) return;
    const inPage = page.options.find((o) => o.value === value);
    if (inPage) {
      setChosen(inPage);
      return;
    }
    let live = true;
    resolveRef.current?.(value).then(
      (o) => live && setChosen(o ?? { value }),
      () => live && setChosen({ value }),
    );
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value]);

  useEffect(() => {
    activeRef.current?.scrollIntoView?.({ block: 'nearest' });
  }, [active]);

  const selected: ComboboxOption | null = value ? (chosen?.value === value ? chosen : { value }) : null;
  const options = page.options;
  const more = useMemo(
    () => (page.total != null && page.total > options.length ? page.total - options.length : 0),
    [page.total, options.length],
  );

  const choose = (o: ComboboxOption) => {
    setChosen(o);
    onChange(o.value);
    setOpen(false);
  };

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      setActive((i) => Math.min(i + 1, options.length - 1));
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setActive((i) => Math.max(i - 1, 0));
    } else if (e.key === 'Enter') {
      e.preventDefault();
      if (options[active]) choose(options[active]);
    }
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      {ariaLabel && (
        <span id={labelId} className="sr-only">
          {ariaLabel}
        </span>
      )}
      <PopoverTrigger
        id={triggerId}
        data-shot="combobox"
        type="button"
        aria-labelledby={ariaLabel ? `${labelId} ${triggerId}` : undefined}
        disabled={disabled}
        className={cn(triggerClasses, className)}
      >
        <span title={selected ? optionLabel(selected) : undefined} className={cn('truncate', !selected && 'text-muted-foreground')}>
          {selected ? optionLabel(selected) : (placeholder ?? t('select'))}
        </span>
        {allowClear && selected && !disabled ? (
          <X
            size={14}
            className="shrink-0 opacity-50 hover:opacity-100"
            role="button"
            aria-label={t('clear')}
            onClick={(e) => {
              e.preventDefault();
              e.stopPropagation();
              setChosen(null);
              onChange('');
            }}
          />
        ) : (
          <ChevronsUpDown size={14} className="shrink-0 opacity-50" />
        )}
      </PopoverTrigger>
      <PopoverContent className={cn(FIELD_POPOVER_WIDTH, 'p-0')} onKeyDown={onKeyDown}>
        <div className="flex items-center border-b border-border">
          <input
            autoFocus
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={searchPlaceholder ?? t('search')}
            className="w-full bg-transparent px-3 py-2 text-sm outline-none placeholder:text-muted-foreground"
          />
          {loading && <Loader2 size={14} className="mr-3 shrink-0 animate-spin opacity-50" aria-label={t('searching')} />}
        </div>
        <div className="max-h-60 overflow-auto p-1" aria-busy={loading}>
          {failed ? (
            <p role="alert" className="px-2 py-3 text-center text-sm text-destructive">
              {t('searchFailed')}
            </p>
          ) : options.length === 0 ? (
            !loading && (
              <p className="px-2 py-3 text-center text-sm text-muted-foreground">{emptyMessage ?? t('noMatches')}</p>
            )
          ) : (
            options.map((o, i) => (
              <button
                key={o.value}
                ref={i === active ? activeRef : undefined}
                data-shot="combobox-option"
                data-shot-value={o.value}
                type="button"
                onMouseEnter={() => setActive(i)}
                onClick={() => choose(o)}
                className={cn(
                  'flex w-full items-start gap-2 rounded-sm px-2 py-1.5 text-left text-sm',
                  i === active ? 'bg-accent text-accent-foreground' : 'text-foreground',
                )}
              >
                <Check size={14} className={cn('mt-0.5 shrink-0', o.value === value ? 'opacity-100' : 'opacity-0')} />
                <span className="min-w-0">
                  <span className="block truncate">{optionLabel(o)}</span>
                  {o.description && <span className="block truncate text-xs text-muted-foreground">{o.description}</span>}
                </span>
              </button>
            ))
          )}
          {more > 0 && !failed && (
            <p className="px-2 py-2 text-center text-xs text-muted-foreground">{t('moreMatches', { count: more })}</p>
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}

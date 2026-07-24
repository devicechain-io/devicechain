// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A searchable single-select dropdown (a "combobox"). Use it wherever a field is
// really a choice from a known set — a tenant token, a role token — instead of a
// free-text Input the user has to type exactly. Built on the Popover primitive;
// no extra dependency beyond @radix-ui/react-popover.

import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Check, ChevronsUpDown, X } from 'lucide-react';
import { cn } from '@/lib/utils';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';

export interface ComboboxOption {
  value: string;
  // Display label; falls back to the value when omitted.
  label?: string;
  // Optional muted secondary line (e.g. an authority pattern or tenant name).
  description?: string;
}

export const optionLabel = (o: ComboboxOption) => o.label ?? o.value;

// filterOptions keeps options whose value/label/description contains the query
// (case-insensitive). Shared with MultiSelect.
export function filterOptions(options: ComboboxOption[], query: string): ComboboxOption[] {
  const q = query.trim().toLowerCase();
  if (!q) return options;
  return options.filter(
    (o) =>
      o.value.toLowerCase().includes(q) ||
      (o.label?.toLowerCase().includes(q) ?? false) ||
      (o.description?.toLowerCase().includes(q) ?? false),
  );
}

const triggerClasses =
  'flex h-10 w-full items-center justify-between gap-2 rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50';

interface ComboboxProps {
  options: ComboboxOption[];
  value: string;
  onChange: (value: string) => void;
  id?: string;
  placeholder?: string;
  searchPlaceholder?: string;
  emptyMessage?: string;
  disabled?: boolean;
  // When true (default), a clear button appears once a value is selected.
  allowClear?: boolean;
  className?: string;
}

export function Combobox({
  options,
  value,
  onChange,
  id,
  // Defaults resolve to the shared `common` strings below (a default parameter
  // can't call the hook), so an unset placeholder is localized, not English.
  placeholder,
  searchPlaceholder,
  emptyMessage,
  disabled,
  allowClear = true,
  className,
}: ComboboxProps) {
  const { t } = useTranslation('common');
  const placeholderText = placeholder ?? t('select');
  const searchPlaceholderText = searchPlaceholder ?? t('search');
  const emptyMessageText = emptyMessage ?? t('noMatches');
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [active, setActive] = useState(0);
  const activeRef = useRef<HTMLButtonElement>(null);

  const filtered = useMemo(() => filterOptions(options, query), [options, query]);
  const selected = options.find((o) => o.value === value) ?? null;

  // Reset the search and highlight each time the popover opens.
  useEffect(() => {
    if (open) {
      setQuery('');
      setActive(0);
    }
  }, [open]);

  useEffect(() => {
    activeRef.current?.scrollIntoView({ block: 'nearest' });
  }, [active]);

  const choose = (v: string) => {
    onChange(v);
    setOpen(false);
  };

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      setActive((i) => Math.min(i + 1, filtered.length - 1));
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setActive((i) => Math.max(i - 1, 0));
    } else if (e.key === 'Enter') {
      e.preventDefault();
      if (filtered[active]) choose(filtered[active].value);
    }
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        id={id}
        type="button"
        disabled={disabled}
        className={cn(triggerClasses, className)}
      >
        <span className={cn('truncate', !selected && 'text-muted-foreground')}>
          {selected ? optionLabel(selected) : placeholderText}
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
              onChange('');
            }}
          />
        ) : (
          <ChevronsUpDown size={14} className="shrink-0 opacity-50" />
        )}
      </PopoverTrigger>
      <PopoverContent className="w-[--radix-popover-trigger-width] p-0" onKeyDown={onKeyDown}>
        <input
          autoFocus
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
            setActive(0);
          }}
          placeholder={searchPlaceholderText}
          className="w-full border-b border-border bg-transparent px-3 py-2 text-sm outline-none placeholder:text-muted-foreground"
        />
        <div className="max-h-60 overflow-auto p-1">
          {filtered.length === 0 ? (
            <p className="px-2 py-3 text-center text-sm text-muted-foreground">{emptyMessageText}</p>
          ) : (
            filtered.map((o, i) => {
              const isSelected = o.value === value;
              return (
                <button
                  key={o.value}
                  ref={i === active ? activeRef : undefined}
                  type="button"
                  onMouseEnter={() => setActive(i)}
                  onClick={() => choose(o.value)}
                  className={cn(
                    'flex w-full items-start gap-2 rounded-sm px-2 py-1.5 text-left text-sm',
                    i === active ? 'bg-accent text-accent-foreground' : 'text-foreground',
                  )}
                >
                  <Check size={14} className={cn('mt-0.5 shrink-0', isSelected ? 'opacity-100' : 'opacity-0')} />
                  <span className="min-w-0">
                    <span className="block truncate">{optionLabel(o)}</span>
                    {o.description && (
                      <span className="block truncate text-xs text-muted-foreground">{o.description}</span>
                    )}
                  </span>
                </button>
              );
            })
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}

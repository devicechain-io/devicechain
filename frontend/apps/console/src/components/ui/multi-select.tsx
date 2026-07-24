// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A searchable multi-select: pick zero or more values from a known set. Selected
// values render as removable pills in the trigger; the popover is a filterable
// checklist that stays open so several can be toggled at once. Use it for fields
// that are really a set of tokens — membership/system role tokens, role
// authorities — instead of a space-separated free-text Input.

import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Check, ChevronsUpDown, X } from 'lucide-react';
import { cn } from '@/lib/utils';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { filterOptions, optionLabel, type ComboboxOption } from '@/components/ui/combobox';

interface MultiSelectProps {
  options: ComboboxOption[];
  value: string[];
  onChange: (value: string[]) => void;
  id?: string;
  placeholder?: string;
  searchPlaceholder?: string;
  emptyMessage?: string;
  disabled?: boolean;
  className?: string;
}

export function MultiSelect({
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
  className,
}: MultiSelectProps) {
  const { t } = useTranslation('common');
  const placeholderText = placeholder ?? t('select');
  const searchPlaceholderText = searchPlaceholder ?? t('search');
  const emptyMessageText = emptyMessage ?? t('noMatches');
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');

  const filtered = useMemo(() => filterOptions(options, query), [options, query]);
  const selectedSet = useMemo(() => new Set(value), [value]);
  const labelOf = useMemo(() => {
    const m = new Map(options.map((o) => [o.value, optionLabel(o)]));
    return (v: string) => m.get(v) ?? v;
  }, [options]);

  useEffect(() => {
    if (open) setQuery('');
  }, [open]);

  const toggle = (v: string) => {
    onChange(selectedSet.has(v) ? value.filter((x) => x !== v) : [...value, v]);
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        id={id}
        type="button"
        disabled={disabled}
        className={cn(
          'flex min-h-10 w-full items-center justify-between gap-2 rounded-md border border-input bg-background px-3 py-1.5 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50',
          className,
        )}
      >
        {value.length === 0 ? (
          <span className="text-muted-foreground">{placeholderText}</span>
        ) : (
          <span className="flex flex-wrap gap-1">
            {value.map((v) => (
              <span
                key={v}
                className="inline-flex items-center gap-1 rounded bg-secondary px-1.5 py-0.5 text-xs text-secondary-foreground"
              >
                {labelOf(v)}
                {!disabled && (
                  <X
                    size={12}
                    className="opacity-60 hover:opacity-100"
                    role="button"
                    aria-label={t('removeItem', { label: labelOf(v) })}
                    onClick={(e) => {
                      e.preventDefault();
                      e.stopPropagation();
                      toggle(v);
                    }}
                  />
                )}
              </span>
            ))}
          </span>
        )}
        <ChevronsUpDown size={14} className="shrink-0 opacity-50" />
      </PopoverTrigger>
      <PopoverContent className="w-[--radix-popover-trigger-width] p-0">
        <input
          autoFocus
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={searchPlaceholderText}
          className="w-full border-b border-border bg-transparent px-3 py-2 text-sm outline-none placeholder:text-muted-foreground"
        />
        <div className="max-h-60 overflow-auto p-1">
          {filtered.length === 0 ? (
            <p className="px-2 py-3 text-center text-sm text-muted-foreground">{emptyMessageText}</p>
          ) : (
            filtered.map((o) => {
              const isSelected = selectedSet.has(o.value);
              return (
                <button
                  key={o.value}
                  type="button"
                  onClick={() => toggle(o.value)}
                  className="flex w-full items-start gap-2 rounded-sm px-2 py-1.5 text-left text-sm text-foreground hover:bg-accent hover:text-accent-foreground"
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

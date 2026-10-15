// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The categories of a bar or pie chart, decided apart from the drawing: which measurements, in
// what order, with which value. One category per measurement, valued by its latest reading.

import type { MeasurementStreamState, WidgetInstance } from '@devicechain/dashboards';

// One category: its label and its value (null = no reading yet).
export interface CategoryItem {
  label: string;
  value: number | null;
}

export const CATEGORY_SORTS = ['none', 'ascending', 'descending'] as const;
export type CategorySort = (typeof CATEGORY_SORTS)[number];

// Unset or unknown keeps the datasource's order. (The widget reads its own option and passes it here.)
export function parseSort(v: unknown): CategorySort {
  return CATEGORY_SORTS.find((s) => s === v) ?? 'none';
}

// The measurements the datasource names, in the order it names them; with none named, every
// measurement that has reported, in the order they were first seen. A category with no reading
// yet is KEPT with a null value (a bar chart shows the label with no bar), so the chart does not
// quietly drop a measurement that has not reported -- but it sorts after those that have.
export function categoryItems(
  widget: WidgetInstance,
  data: Pick<MeasurementStreamState, 'latest'>,
  sort: CategorySort,
): CategoryItem[] {
  const named = widget.datasource?.measurements ?? [];
  const names = named.length > 0 ? [...new Set(named)] : Object.keys(data.latest);
  const items = names.map((label) => ({ label, value: data.latest[label]?.value ?? null }));
  if (sort === 'none') return items;
  const sign = sort === 'ascending' ? 1 : -1;
  return items
    .map((item, i) => ({ item, i }))
    .sort((a, b) => {
      if (a.item.value === null || b.item.value === null) {
        return a.item.value === b.item.value ? a.i - b.i : a.item.value === null ? 1 : -1;
      }
      return sign * (a.item.value - b.item.value) || a.i - b.i;
    })
    .map((x) => x.item);
}

// Why a pie cannot be drawn from these items, or undefined when it can. A pie is shares of a
// whole: a negative value has no share, and all zeros have nothing to divide.
export function pieProblem(items: readonly CategoryItem[]): 'negative' | 'noShare' | undefined {
  const values = items.map((i) => i.value).filter((v): v is number => v !== null);
  if (values.some((v) => v < 0)) return 'negative';
  if (values.length > 0 && values.every((v) => v === 0)) return 'noShare';
  return undefined;
}

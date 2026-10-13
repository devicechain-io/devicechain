// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The "Add widget" menu: one entry per descriptor the REGISTRY holds, grouped by category,
// each showing its title and which widget (and which release of it) it would add. The menu
// owns no list of widgets — a package composed into the host's registry appears here with no
// change to this file, and a registered widget cannot be missing from it.
//
// Text follows the same rule as the settings fields: the console's message for the id the
// descriptor declares, else the text the package shipped.

import type { WidgetDescriptor } from '@devicechain/dashboards';
import { Plus } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';

import { resolveText, useMessageLookup } from './optionWording';
import type { EditorWidgetRegistry } from './widgetRegistry';

// Descriptors without a category share one group, listed last, so an uncategorized
// extension is still reachable and never hidden behind a heading it did not choose.
const UNCATEGORIZED = '';

export function AddWidgetMenu({
  registry,
  onAdd,
}: {
  registry: EditorWidgetRegistry;
  onAdd: (descriptor: WidgetDescriptor) => void;
}) {
  const { t } = useTranslation('dashboards');
  const lookup = useMessageLookup();

  const groups = new Map<string, WidgetDescriptor[]>();
  for (const { descriptor } of registry.list()) {
    const category = resolveText(lookup, descriptor.display.categoryId, descriptor.display.category) ?? UNCATEGORIZED;
    groups.set(category, [...(groups.get(category) ?? []), descriptor]);
  }
  const ordered = [...groups.entries()].sort(([a], [b]) =>
    a === b ? 0 : a === UNCATEGORIZED ? 1 : b === UNCATEGORIZED ? -1 : 0,
  );

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" size="sm" data-shot="dashboard-add-widget">
          <Plus size={14} /> {t('workspaceAddWidget')}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {ordered.map(([category, descriptors]) => (
          <DropdownMenuGroup key={category} data-testid="add-widget-group">
            {category !== UNCATEGORIZED && <DropdownMenuLabel>{category}</DropdownMenuLabel>}
            {descriptors.map((descriptor) => (
              <DropdownMenuItem
                key={descriptor.id}
                data-testid={`add-widget-${descriptor.id}`}
                onSelect={() => onAdd(descriptor)}
              >
                <span className="flex flex-col">
                  <span>{resolveText(lookup, descriptor.display.titleId, descriptor.display.title)}</span>
                  {/* Which widget and release this adds: the id is what the stored
                      definition records, and the version is provenance for an author
                      choosing between a built-in and a package's widget. */}
                  <span className="text-xs text-muted-foreground">
                    {t('widgetMenuDetail', { id: descriptor.id, version: descriptor.version })}
                  </span>
                </span>
              </DropdownMenuItem>
            ))}
          </DropdownMenuGroup>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

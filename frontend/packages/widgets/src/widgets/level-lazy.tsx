// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The level-and-indicator registrations: descriptor eager, component lazy. The descriptors
// are the catalog (the editor, the validators and WIDGET_TYPES read them without rendering
// anything), so they stay in the entry; the five components load on first mount, together,
// from one chunk (level-widgets.ts), so a board without one of them never downloads it.
//
// Until the chunk arrives the widget renders nothing inside its box. A chunk that fails to
// load is THROWN, so the widget's error boundary shows the failure: an empty tank that looks
// like a reading is the outcome this must never produce.

import { useEffect, useState } from 'react';
import { defineWidget, type WidgetProps } from '@devicechain/widget-sdk';

import { builtinWidgetDescriptors } from '../builtin-descriptors';

type LevelWidgets = typeof import('./level-widgets');

let loaded: LevelWidgets | undefined;
let loading: Promise<LevelWidgets> | undefined;

// Memoized: every level widget on a board shares one fetch. Once it has resolved, `loaded`
// lets a later mount render synchronously, with no empty first frame.
export function loadLevelWidgets(): Promise<LevelWidgets> {
  loading ??= import('./level-widgets').then((m) => (loaded = m));
  return loading;
}

function lazyComponent(name: keyof LevelWidgets) {
  function LazyLevelWidget(props: WidgetProps) {
    const [mod, setMod] = useState(loaded);
    const [error, setError] = useState<unknown>();
    useEffect(() => {
      if (!mod) loadLevelWidgets().then(setMod, setError);
    }, [mod]);
    if (error !== undefined) throw error;
    if (!mod) return null;
    const Component = mod[name];
    return <Component {...props} />;
  }
  LazyLevelWidget.displayName = `Lazy(${name})`;
  return LazyLevelWidget;
}

export const linearGaugeWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['linear-gauge'],
  implementation: { kind: 'trusted-react', component: lazyComponent('LinearGauge') },
});

export const levelGaugeWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['level-gauge'],
  implementation: { kind: 'trusted-react', component: lazyComponent('LevelGauge') },
});

export const batteryWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['battery'],
  implementation: { kind: 'trusted-react', component: lazyComponent('Battery') },
});

export const statusLightWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['status-light'],
  implementation: { kind: 'trusted-react', component: lazyComponent('StatusLight') },
});

export const signalBarsWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['signal-bars'],
  implementation: { kind: 'trusted-react', component: lazyComponent('SignalBars') },
});

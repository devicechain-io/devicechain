// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Reads what a device type's profile declares, for the board generator.
//
// "What the profile declares" has two answers, and they differ on purpose:
//   - the ACTIVE PUBLISHED version is what ingest validates against and what a command is
//     checked against when it is enqueued. The API exposes it per DEVICE, so when the type has a
//     device it is read through that device, and the board matches what the type really accepts;
//   - the DRAFT is what the author has written down, readable per profile. It is the fallback
//     for a type with no device yet (or a profile never published), and the caller is told so,
//     because a draft can name a metric ingest does not yet know.
// The published command list carries no `impact`, so it is joined with the draft by command key;
// a published command with no draft row has an unknown impact, which the generator treats as not
// safe to put a button on.

import {
  getDeviceCommandVocabulary,
  getDeviceMetricVocabulary,
  listCommandDefinitions,
  listDevices,
  listMetricDefinitions,
} from '@/lib/api/device-management';
import type { BoardCommand, BoardMetric } from './generate';

export interface BoardSource {
  metrics: BoardMetric[];
  commands: BoardCommand[];
  defaultDevice: string | null;
  basis: 'published' | 'draft';
}

export async function loadBoardSource(typeToken: string, profileToken: string): Promise<BoardSource> {
  const [devices, draftCommands] = await Promise.all([
    listDevices({ pageNumber: 1, pageSize: 1, deviceType: typeToken }),
    listCommandDefinitions(profileToken),
  ]);
  const defaultDevice = devices.results[0]?.token ?? null;
  const impactOf = new Map(draftCommands.map((c) => [c.commandKey, c.impact]));

  if (defaultDevice) {
    const [metrics, commands] = await Promise.all([
      getDeviceMetricVocabulary(defaultDevice),
      getDeviceCommandVocabulary(defaultDevice),
    ]);
    if (metrics && metrics.length > 0) {
      return {
        basis: 'published',
        defaultDevice,
        metrics: metrics.map((m) => ({
          key: m.metricKey,
          dataType: m.dataType,
          unit: m.unit,
          calculated: m.calculated,
        })),
        // Unconstrained means the profile restricts nothing: there is no list to build buttons from.
        commands: (commands?.constrained ? commands.commands : []).map((c) => ({
          key: c.commandKey,
          name: c.name,
          description: c.description,
          parameterSchema: c.parameterSchema,
          impact: impactOf.get(c.commandKey) ?? null,
        })),
      };
    }
  }

  const draftMetrics = await listMetricDefinitions(profileToken);
  return {
    basis: 'draft',
    defaultDevice,
    metrics: draftMetrics.map((m) => ({
      key: m.metricKey,
      dataType: m.dataType,
      unit: m.unit,
      calculated: m.calculated,
    })),
    commands: draftCommands.map((c) => ({
      key: c.commandKey,
      name: c.name,
      description: c.description,
      parameterSchema: c.parameterSchema,
      impact: c.impact,
    })),
  };
}

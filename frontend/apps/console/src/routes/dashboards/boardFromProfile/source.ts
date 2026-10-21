// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Reads what a device type's profile declares, for the board generator.
//
// "What the profile declares" has two answers, and they differ on purpose:
//   - the ACTIVE PUBLISHED version is what ingest validates against and what a command is
//     checked against when it is enqueued. It is read by the profile's token, so a type with
//     no devices yet gets it too, and every field comes from that one frozen version,
//     including each command's impact, which decides whether it gets a button;
//   - the DRAFT is what the author has written down. It is used ONLY when the profile has
//     never been published, and the caller is told so, because a draft can name a metric
//     ingest does not yet know.
// A published version that declares no metrics is a real answer (the generator refuses an
// empty board); it is never swapped for the draft.

import {
  getProfilePublishedVocabulary,
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
  const [devices, published] = await Promise.all([
    listDevices({ pageNumber: 1, pageSize: 1, deviceType: typeToken }),
    getProfilePublishedVocabulary(profileToken),
  ]);
  const defaultDevice = devices.results[0]?.token ?? null;

  if (published) {
    return {
      basis: 'published',
      defaultDevice,
      metrics: published.metrics.map((m) => ({
        key: m.metricKey,
        dataType: m.dataType,
        unit: m.unit,
        calculated: m.calculated,
      })),
      commands: published.commands.map((c) => ({
        key: c.commandKey,
        name: c.name,
        description: c.description,
        parameterSchema: c.parameterSchema,
        impact: c.impact,
      })),
    };
  }

  const [draftMetrics, draftCommands] = await Promise.all([
    listMetricDefinitions(profileToken),
    listCommandDefinitions(profileToken),
  ]);
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

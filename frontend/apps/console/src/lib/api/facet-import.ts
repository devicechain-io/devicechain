// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operation for importFacetValues: sets facet values on many existing
// entities, addressed by token, in one request of at most 1000 rows. Each row is its own
// transaction on the server and gets its own result; a dry run decides every row the same
// way and writes nothing.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-management';
import type { FacetValueImportRow } from '@/gql/device-management/graphql';

export type { FacetValueImportRow };

/** SET (would set on a dry run), EXISTS (unchanged), INVALID or REFUSED. */
export type FacetImportOutcome = 'SET' | 'EXISTS' | 'INVALID' | 'REFUSED';

export interface FacetValueImportRowResult {
  /** 1-based position within the call. */
  row: number;
  memberType: string;
  token: string;
  key: string;
  outcome: FacetImportOutcome;
  code?: string | null;
  message?: string | null;
}

const IMPORT_FACET_VALUES = graphql(`
  mutation ImportFacetValues($rows: [FacetValueImportRow!]!, $dryRun: Boolean!) {
    importFacetValues(rows: $rows, dryRun: $dryRun) {
      row
      memberType
      token
      key
      outcome
      code
      message
    }
  }
`);

export async function importFacetValues(
  rows: FacetValueImportRow[],
  dryRun: boolean,
): Promise<FacetValueImportRowResult[]> {
  const data = await gql('device-management', IMPORT_FACET_VALUES, { rows, dryRun });
  return data.importFacetValues as FacetValueImportRowResult[];
}

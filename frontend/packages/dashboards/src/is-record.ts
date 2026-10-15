// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// isRecord is the one "plain JSON object" test the parsers share: an object that is neither null
// nor an array. One copy, because every module that reads a raw document needs it and each private
// copy is shipped again in every bundle that parses a board.
export function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

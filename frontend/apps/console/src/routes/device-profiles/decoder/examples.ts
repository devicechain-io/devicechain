// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Example text shown as placeholders in the Decoder tab. These are CEL, a JSON path, a hex frame and a
// media type: technical samples, identical in every language, so they are not translated.

export const EXAMPLE = {
  jsonPath: 'sensor.temperature',
  rowWhen: 'hdr == 1',
  celExpr: 'double(u8(frame, 5)) / 2.0',
  mapExpr: 'lpp.decode(slice(frame, 4, size(frame) - 4))',
  prefix: 'lpp.',
  sectionWhen: 'fCnt > 0',
  contentType: 'application/json',
  frameHex: '01 00 D7 00',
} as const;

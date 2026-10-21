// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The alarm components, gathered into ONE module so they ship as one lazily loaded chunk. The
// registry reaches it only through the dynamic import in alarm-lazy.tsx.

export { AlarmTable } from './alarm-table';
export { AlarmCount } from './alarm-count';
export { AlarmSeverity } from './alarm-severity';
export { GroupedAlarm } from './alarm-grouped';

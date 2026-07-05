// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Controlled vocabularies for the device-profile authoring forms. These mirror the
// backend enums (device-management model): metric data types (ADR-016), alarm
// operators / severities / condition types (ADR-041). Kept here as option arrays so
// the Combobox controls stay a single source of truth; the backend still validates.

export interface Option {
  value: string;
  label: string;
  description?: string;
}

// Metric data types storable as a time-series measurement (ADR-016 amd): STRING is
// device state, not a metric, so it is deliberately not offered here.
export const METRIC_DATA_TYPES: Option[] = [
  { value: 'DOUBLE', label: 'Double', description: 'Floating-point measurement' },
  { value: 'INT', label: 'Integer', description: 'Whole-number measurement' },
  { value: 'BOOLEAN', label: 'Boolean', description: 'On/off, stored as 0/1' },
];

// Alarm comparison operators (ADR-041). Labels use plain math symbols; the value is
// the backend token.
export const ALARM_OPERATORS: Option[] = [
  { value: 'GT', label: '> greater than' },
  { value: 'GTE', label: '≥ greater than or equal' },
  { value: 'LT', label: '< less than' },
  { value: 'LTE', label: '≤ less than or equal' },
  { value: 'EQ', label: '= equal to' },
  { value: 'NEQ', label: '≠ not equal to' },
];

// Alarm severities, most-to-least severe (ADR-041 rank order).
export const ALARM_SEVERITIES: Option[] = [
  { value: 'CRITICAL', label: 'Critical' },
  { value: 'MAJOR', label: 'Major' },
  { value: 'MINOR', label: 'Minor' },
  { value: 'WARNING', label: 'Warning' },
  { value: 'INDETERMINATE', label: 'Indeterminate' },
];

// Alarm condition types. Only SIMPLE (threshold) is evaluated today (ADR-041); the
// DURATION / REPEATING variants are modeled but not yet evaluated, so the authoring
// form offers SIMPLE alone rather than letting a user create a rule that won't fire.
export const ALARM_CONDITION_SIMPLE = 'SIMPLE';

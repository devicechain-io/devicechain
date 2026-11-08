// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// How an alarm names the thing it was raised on and prints its value, shared by the tenant
// alarm list and the alarms tab on a device.
import { Link } from 'react-router-dom';
import { formatValue } from '@devicechain/widget-sdk';

interface Originator {
  originatorType: string;
  originatorId: string;
  originatorToken?: string | null;
}

/** The originator's token, or "type #id" when it cannot be resolved (deleted, or not a device). */
export function originatorLabel(a: Originator): string {
  if (a.originatorToken) return a.originatorToken;
  return `${a.originatorType} #${a.originatorId}`;
}

/** The path of the device an alarm was raised on, or null when the originator is not a device. */
export function originatorDevicePath(a: Originator): string | null {
  if (a.originatorType.toLowerCase() !== 'device' || !a.originatorToken) return null;
  return `/devices/${encodeURIComponent(a.originatorToken)}`;
}

/** The originator as a link to its device page when it is a device, plain text otherwise. */
export function OriginatorLink({ alarm }: { alarm: Originator }) {
  const path = originatorDevicePath(alarm);
  const label = originatorLabel(alarm);
  if (!path) return <>{label}</>;
  return (
    <Link to={path} className="text-primary underline-offset-2 hover:underline" data-shot="alarm-device-link">
      {label}
    </Link>
  );
}

/**
 * An alarm's value as the dashboard prints a metric, followed by the unit its profile declares
 * for the metric when one is known. No value is a dash, with no unit after it.
 */
export function formatAlarmValue(value: number | null | undefined, unit?: string | null): string {
  const text = formatValue(value);
  return value == null || !unit ? text : `${text} ${unit}`;
}

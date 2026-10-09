// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

export interface Measured {
  initial: number;
  total: number;
}
export interface Budget {
  maxInitial: number;
  maxTotal: number;
  recordingGzip: number;
  hardInitial: number;
  hardTotal: number;
}
export function initialFiles(dist: string): string[];
export function measure(dist: string): Measured;
export function check(measured: Measured, budget: Budget): string[];

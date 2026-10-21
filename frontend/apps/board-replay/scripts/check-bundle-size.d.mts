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
export interface LazyBoundary {
  what: string;
  chunk: string;
  markers: string[];
}
export function lazyBoundaries(): LazyBoundary[];
export function checkShape(dist: string, boundaries: LazyBoundary[]): string[];
export interface AbsentBoundary {
  what: string;
  reach: string;
  markers: string[];
}
export function absentBoundaries(): AbsentBoundary[];
export function checkAbsent(dist: string, boundaries: AbsentBoundary[]): string[];

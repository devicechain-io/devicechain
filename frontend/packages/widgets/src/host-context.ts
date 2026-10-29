// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// `@devicechain/widgets/host-context` -- the two provider seams a host installs at the TOP of its
// tree (the tenant basemap and the map runtime), and nothing else.
//
// Why a separate entry: the main entry registers every built-in widget as top-level code a
// bundler cannot prove side-effect free, so importing even these two providers from it drags the
// whole widget library -- and the dashboards runtime beneath it -- into the host's FIRST download.
// A host that renders widgets only on lazy routes imports its providers from here and keeps the
// library out of the initial load. The main entry re-exports the very same modules, so both
// entries share one context instance.
export { TenantBasemapProvider, useTenantBasemap } from './basemap-context';
export { MapRuntimeProvider, useMapRuntime, type MapRuntime } from './map-runtime-context';

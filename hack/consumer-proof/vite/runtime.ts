// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Vite arm's map runtime: the ready-made one the package ships.
//
// This is the whole point of `@devicechain/widgets/vite` — a Vite consumer writes one
// import and no bundler dialect of their own. If this file ever needs more than a
// re-export, the convenience the subpath exists to provide has stopped existing.

export { viteMapRuntime as mapRuntime } from '@devicechain/widgets/vite';

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The chunk the presence widgets load from (presence-lazy.tsx): the device status card and the
// online doughnut, with no chart engine, so a board without one never downloads them. The feed that
// reads their data (presence-feed.tsx) rides in the same chunk: both widgets always need it, so a
// chunk of its own would only cost the first download one more name to carry.

export { DeviceStatus } from './device-status';
export { OnlineDoughnut } from './online-doughnut';
export { default as PresenceFeed } from '../presence-feed';

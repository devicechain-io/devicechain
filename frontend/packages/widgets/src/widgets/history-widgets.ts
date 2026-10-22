// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The chunk the history table loads from (history-lazy.tsx): the table and the feed that reads its
// buckets (history-feed.tsx), with no chart engine, so a board without a history table never
// downloads either. Both are always needed together, so they share one chunk.

export { HistoryTable } from './history-table';
export { default as HistoryFeed } from '../history-feed';

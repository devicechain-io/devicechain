// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The typed measurementStream subscription document, hand-authored.
//
// Packages carry no graphql-codegen (only apps do), so unlike the console this
// document is written by hand. The SDK uses documentMode:'string' — it only ever
// calls document.toString() and sends the text over graphql-ws — so a raw
// GraphQL string carrying phantom result/variable types IS exactly what a
// generated TypedDocumentString would be at runtime, minus the class wrapper.

import type { DocumentTypeDecoration } from '@graphql-typed-document-node/core';

import type { MeasurementSample } from '../types';

// A GraphQL document string tagged with its result/variable types — the shape
// the SDK's gql()/subscribe() accept (they only need .toString()). Local to this
// file; the SDK inlines the same shape but does not export it (a worthwhile
// future SDK re-export would let both sides share one alias).
type TypedDocument<TResult, TVariables> = DocumentTypeDecoration<TResult, TVariables> & {
  toString(): string;
};

export interface MeasurementStreamResult {
  measurementStream: MeasurementSample;
}

export interface MeasurementStreamVariables {
  deviceId?: string | null;
  name?: string | null;
}

export const MEASUREMENT_STREAM = `
  subscription MeasurementStream($deviceId: String, $name: String) {
    measurementStream(deviceId: $deviceId, name: $name) {
      id
      deviceId
      eventType
      occurredTime
      name
      value
      classifier
    }
  }
` as unknown as TypedDocument<MeasurementStreamResult, MeasurementStreamVariables>;

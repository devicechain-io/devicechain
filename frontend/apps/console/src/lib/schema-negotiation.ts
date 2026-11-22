// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Whether the console checks its requests against the schema the connected instance serves
// (see @devicechain/client enableSchemaNegotiation).
//
// A console served by its own instance, at the same origin as the GraphQL endpoints it calls,
// is the release that instance runs: every document it sends is one that instance serves, so the
// check can only add a request per endpoint and a way to fail (a proxy or WAF that answers
// OPTIONS itself makes discovery fail closed). It is therefore OFF in a production build.
//
// It is ON where the console and the instance can be different releases: `vite dev` (the dev
// proxy can point at any instance), and a build made with VITE_SCHEMA_NEGOTIATION=on (a console
// hosted apart from its instance). VITE_SCHEMA_NEGOTIATION=off turns it off even in dev.

export interface NegotiationEnv {
  DEV?: boolean;
  VITE_SCHEMA_NEGOTIATION?: string;
}

export function schemaNegotiationWanted(env: NegotiationEnv): boolean {
  const setting = env.VITE_SCHEMA_NEGOTIATION?.trim().toLowerCase();
  if (setting === 'on' || setting === 'true') return true;
  if (setting === 'off' || setting === 'false') return false;
  return env.DEV === true;
}

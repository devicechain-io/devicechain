// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Classification of transport failures the UI has to render differently.
//
// The implementation moved into @devicechain/client, beside the
// GraphQLRequestError it inspects, once a second consumer appeared: the dashboard
// runtime's location channel needs the same "refused, not failed" distinction the
// device-position panel does, and a copy in the console would have been a copy
// the packages could not import. Re-exported here so `@/lib/api/errors` stays the
// console's name for it.
export { isForbiddenError } from '@devicechain/client';

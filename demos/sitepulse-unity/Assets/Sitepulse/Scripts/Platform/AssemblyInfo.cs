// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Runtime.CompilerServices;

// ObservedState is written only inside this assembly (by the observer's inbox drain); the tests
// are the one outside reader of those writers.
[assembly: InternalsVisibleTo("DeviceChain.Sitepulse.Tests.EditMode")]

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Runtime.CompilerServices;

// ObservedState is written only inside this assembly (by the observer's inbox drain); the tests
// are the one outside reader of those writers. The Sitepulse test assembly is the only one that
// reads them so far; a demo that adds tests of its own adds its assembly name here.
[assembly: InternalsVisibleTo("DeviceChain.Sitepulse.Tests.EditMode")]

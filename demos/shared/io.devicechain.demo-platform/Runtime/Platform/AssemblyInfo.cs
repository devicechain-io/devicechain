// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Runtime.CompilerServices;

// ObservedState is written only inside this assembly (by the observer's inbox drain); the tests
// are the one outside reader of those writers. Sitepulse's EditMode suite drives the observer end to
// end through them, so it is named here until those tests move into this package.
[assembly: InternalsVisibleTo("DeviceChain.Demos.Tests")]
[assembly: InternalsVisibleTo("DeviceChain.Sitepulse.Tests.EditMode")]

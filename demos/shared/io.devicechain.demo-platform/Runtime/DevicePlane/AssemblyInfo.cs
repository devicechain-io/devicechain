// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Runtime.CompilerServices;

// The tests build a link over a carrier of their own (SdkDeviceLink.OverCarrier) to see exactly what it
// asks the SDK to send. The Sitepulse test assembly is the only one that does so far; a demo that adds
// tests of its own adds its assembly name here.
[assembly: InternalsVisibleTo("DeviceChain.Sitepulse.Tests.EditMode")]

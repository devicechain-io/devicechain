// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Runtime.CompilerServices;

// SdkDeviceLink's seam over the SDK (the state mapping, a link over a caller's carrier) is internal so the
// SDK-boundary tests can see exactly what it asks the SDK to send. Sitepulse's suite holds those tests today.
[assembly: InternalsVisibleTo("DeviceChain.Demos.Tests")]
[assembly: InternalsVisibleTo("DeviceChain.Sitepulse.Tests.EditMode")]

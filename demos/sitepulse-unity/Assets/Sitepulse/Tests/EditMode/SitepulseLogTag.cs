// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Demos.Platform;
using DeviceChain.Sitepulse.Platform;
using NUnit.Framework;

// No namespace on purpose: a SetUpFixture outside every namespace runs once for the whole assembly.
// The runtime installs the tag at player start (SitepulseLog), and that does not happen in an EditMode
// run, so the tests that expect a "[sitepulse]" line install it here.
[SetUpFixture]
public sealed class SitepulseLogTag
{
    [OneTimeSetUp]
    public void InstallTag() => PlatformLog.Prefix = SitepulseLog.Tag;
}

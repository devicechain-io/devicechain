// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Sdk;
using Xunit;

namespace DeviceChain.Sdk.Tests;

public class AreaTests
{
    [Fact]
    public void Path_matches_the_ingress_contract()
    {
        Assert.Equal("/api/device-management/graphql", Areas.Path(Area.DeviceManagement));
        Assert.Equal("/api/user-management/graphql", Areas.Path(Area.UserManagement));
    }
}

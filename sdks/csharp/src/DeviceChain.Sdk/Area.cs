// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

namespace DeviceChain.Sdk;

/// <summary>
/// A DeviceChain functional area that exposes a GraphQL endpoint. The cluster ingress
/// routes <c>/api/{area}/graphql</c> to each service (mirrors the TS SDK's <c>Area</c>).
/// </summary>
public enum Area
{
    UserManagement,
    DeviceManagement,
    EventManagement,
    DeviceState,
    CommandDelivery,
    DashboardManagement,
}

/// <summary>Maps an <see cref="Area"/> to its ingress path segment.</summary>
public static class Areas
{
    /// <summary>The relative GraphQL path for an area, e.g. <c>/api/device-management/graphql</c>.</summary>
    public static string Path(Area area) => $"/api/{Segment(area)}/graphql";

    /// <summary>The ingress path segment for an area (the kebab-case service name).</summary>
    public static string Segment(Area area) => area switch
    {
        Area.UserManagement => "user-management",
        Area.DeviceManagement => "device-management",
        Area.EventManagement => "event-management",
        Area.DeviceState => "device-state",
        Area.CommandDelivery => "command-delivery",
        Area.DashboardManagement => "dashboard-management",
        _ => throw new System.ArgumentOutOfRangeException(nameof(area), area, "unknown area"),
    };
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sdk;

/// <summary>One entry from a GraphQL response's <c>errors</c> array.</summary>
public sealed class GraphQlError
{
    public string Message { get; set; } = "";
}

/// <summary>
/// Thrown when a GraphQL request fails — either transport-level (a non-2xx HTTP status, a
/// socket failure; <see cref="Status"/> is the HTTP code, or 0) or a GraphQL-level
/// <c>errors</c> payload (<see cref="Errors"/> carries them). Mirrors the TS SDK's
/// <c>GraphQLRequestError</c>.
/// </summary>
public sealed class GraphQlRequestException : Exception
{
    public int Status { get; }
    public IReadOnlyList<GraphQlError> Errors { get; }

    public GraphQlRequestException(string message, int status = 0, IReadOnlyList<GraphQlError>? errors = null)
        : base(message)
    {
        Status = status;
        Errors = errors ?? Array.Empty<GraphQlError>();
    }
}

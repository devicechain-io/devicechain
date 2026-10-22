// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Threading;
using System.Threading.Tasks;

namespace DeviceChain.Demos.Platform
{
    /// <summary>Runs one operator GraphQL query and returns the response's <c>data</c> as JSON text.</summary>
    public delegate Task<string> QueryFn(string query, string variablesJson, CancellationToken ct);
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>
    /// Reads what the platform last observed for each device (device-state's
    /// <c>latestMeasurements</c>) in one aliased query, so a machine can resume from it. The parse is
    /// pure over the response text. A device the platform has nothing for is simply absent from the
    /// result, and a query that fails returns no state at all (and says why) rather than guessing.
    /// </summary>
    public static class LastStateQuery
    {
        public static string Build(IReadOnlyList<string> deviceTokens)
        {
            var sb = new StringBuilder("query Last(");
            for (var i = 0; i < deviceTokens.Count; i++) sb.Append(i == 0 ? "" : ", ").Append("$t").Append(i).Append(": String!");
            sb.Append(") {");
            for (var i = 0; i < deviceTokens.Count; i++) sb.Append(" d").Append(i).Append(": latestMeasurements(deviceToken: $t").Append(i).Append(") { name value }");
            return sb.Append(" }").ToString();
        }

        public static string Variables(IReadOnlyList<string> deviceTokens) =>
            VarsJson.Strings(deviceTokens, i => "t" + i);

        /// <summary>device token to (measurement name to value). Rows without a numeric value are skipped.</summary>
        public static IReadOnlyDictionary<string, IReadOnlyDictionary<string, double>> Parse(IReadOnlyList<string> deviceTokens, string dataJson)
        {
            var result = new Dictionary<string, IReadOnlyDictionary<string, double>>(StringComparer.Ordinal);
            using var doc = JsonDocument.Parse(dataJson);
            for (var i = 0; i < deviceTokens.Count; i++)
            {
                if (!doc.RootElement.TryGetProperty("d" + i, out var rows) || rows.ValueKind != JsonValueKind.Array) continue;
                var values = new Dictionary<string, double>(StringComparer.Ordinal);
                foreach (var row in rows.EnumerateArray())
                {
                    var name = DeviceBinder.Str(row, "name");
                    if (name == null || !row.TryGetProperty("value", out var v) || v.ValueKind != JsonValueKind.Number) continue;
                    if (v.TryGetDouble(out var d)) values[name] = d;
                }

                if (values.Count > 0) result[deviceTokens[i]] = values;
            }

            return result;
        }

        /// <summary>Run the query; a failure is logged and answers an empty set, so every device falls back to its seed.</summary>
        public static async Task<IReadOnlyDictionary<string, IReadOnlyDictionary<string, double>>> FetchAsync(QueryFn query, IReadOnlyList<string> deviceTokens, CancellationToken ct)
        {
            var none = new Dictionary<string, IReadOnlyDictionary<string, double>>(StringComparer.Ordinal);
            if (deviceTokens.Count == 0) return none;
            try
            {
                var data = await query(Build(deviceTokens), Variables(deviceTokens), ct);
                return Parse(deviceTokens, data);
            }
            catch (OperationCanceledException) { throw; }
            catch (Exception e)
            {
                PlatformLog.Warn($"the platform's last observed values could not be read ({e.GetType().Name}: {e.Message}); machines start from their seed");
                return none;
            }
        }
    }
}

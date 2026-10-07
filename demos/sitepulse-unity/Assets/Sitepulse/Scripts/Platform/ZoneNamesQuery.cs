// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>What reading the zones' names from the platform came to: the names it gave, the zones it did not name, and why it could not answer (when it could not).</summary>
    public sealed class ZoneNamesResult
    {
        public ZoneNamesResult(IReadOnlyDictionary<string, string> names, IReadOnlyList<string> unnamed, string failure)
        {
            Names = names;
            Unnamed = unnamed;
            Failure = failure;
        }

        /// <summary>area token to the platform's name for the area.</summary>
        public IReadOnlyDictionary<string, string> Names { get; }

        /// <summary>Zones the platform has no area (or no name) for. They get no label.</summary>
        public IReadOnlyList<string> Unnamed { get; }

        /// <summary>Why the platform could not be asked, or null when it answered.</summary>
        public string Failure { get; }

        public bool Ok => Failure == null;
    }

    /// <summary>
    /// Reads the platform's area names (device-management's <c>areasByToken</c>, read-only, under the operator's own token) for the site's
    /// zones. The parse is pure over the response text. A zone the platform does not name, or a query that fails, yields no name (and says
    /// why): the scene shows no label for it, never a guessed one.
    /// </summary>
    public static class ZoneNamesQuery
    {
        public const string Document = "query Zones($t: [String!]!) { areasByToken(tokens: $t) { token name } }";

        public static string Variables(IEnumerable<string> zoneTokens) => VarsJson.StringList("t", zoneTokens);

        public static ZoneNamesResult Parse(ICollection<string> zoneTokens, string dataJson)
        {
            var names = new Dictionary<string, string>(StringComparer.Ordinal);
            using var doc = JsonDocument.Parse(dataJson);
            if (doc.RootElement.TryGetProperty("areasByToken", out var rows) && rows.ValueKind == JsonValueKind.Array)
                foreach (var row in rows.EnumerateArray())
                {
                    var token = DeviceBinder.Str(row, "token");
                    var name = DeviceBinder.Str(row, "name");
                    if (token != null && !string.IsNullOrWhiteSpace(name)) names[token] = name;
                }

            var unnamed = new List<string>();
            foreach (var t in zoneTokens)
                if (!names.ContainsKey(t)) unnamed.Add(t);
            unnamed.Sort(StringComparer.Ordinal);
            return new ZoneNamesResult(names, unnamed, null);
        }

        /// <summary>Runs the query. A failure is logged and answers no names at all, with the reason.</summary>
        public static async Task<ZoneNamesResult> FetchAsync(QueryFn query, ICollection<string> zoneTokens, CancellationToken ct)
        {
            var none = new Dictionary<string, string>(StringComparer.Ordinal);
            var tokens = new List<string>(zoneTokens);
            tokens.Sort(StringComparer.Ordinal);
            try
            {
                var data = await query(Document, Variables(tokens), ct);
                var result = Parse(tokens, data);
                if (result.Unnamed.Count > 0)
                    PlatformLog.Warn("the platform names no area for " + string.Join(", ", result.Unnamed) + ": those zones have no label");
                return result;
            }
            catch (OperationCanceledException) { throw; }
            catch (Exception e)
            {
                var why = e.GetType().Name + ": " + e.Message;
                PlatformLog.Warn($"the platform's area names could not be read ({why}); the zones have no labels");
                return new ZoneNamesResult(none, tokens, why);
            }
        }
    }
}

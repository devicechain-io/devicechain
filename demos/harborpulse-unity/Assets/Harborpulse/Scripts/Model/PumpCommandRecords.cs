// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Text.Json;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>Hand-read command-delivery page. Optional timestamps stay absent; never substitute observation time.</summary>
    public static class PumpCommandRecords
    {
        public static IReadOnlyList<HarborCommand> Parse(JsonElement page, string externalId, string boundToken, DateTimeOffset observedAt)
        {
            var rows = Field(page, "results");
            if (rows.ValueKind != JsonValueKind.Array) throw new FormatException("command results must be an array");
            var result = new List<HarborCommand>();
            foreach (var row in rows.EnumerateArray())
            {
                var device = Text(row, "deviceToken");
                if (device != boundToken) throw new FormatException("command device does not match bound pump");
                var status = Text(row, "status");
                if (Array.IndexOf(new[] { "QUEUED", "HELD", "SENT", "PARKED", "SUCCESSFUL", "FAILED", "TIMEOUT", "EXPIRED", "CANCELLED" }, status) < 0)
                    throw new FormatException("unknown command status");
                result.Add(new HarborCommand(externalId, Text(row, "token"), Text(row, "name"), status, observedAt,
                    Time(row, "queuedTime"), Time(row, "sentTime"), Time(row, "respondedTime"), device));
            }
            return result.AsReadOnly();
        }
        static JsonElement Field(JsonElement row, string name)
        {
            if (row.ValueKind != JsonValueKind.Object || !row.TryGetProperty(name, out var value)) throw new FormatException("command field missing: " + name);
            return value;
        }
        static string Text(JsonElement row, string name)
        {
            var value = Field(row, name);
            if (value.ValueKind != JsonValueKind.String || string.IsNullOrEmpty(value.GetString())) throw new FormatException("command field must be a string: " + name);
            return value.GetString();
        }
        static DateTimeOffset? Time(JsonElement row, string name)
        {
            if (!row.TryGetProperty(name, out var value) || value.ValueKind == JsonValueKind.Null) return null;
            if (value.ValueKind != JsonValueKind.String || !value.TryGetDateTimeOffset(out var at)) throw new FormatException("command timestamp invalid: " + name);
            return at;
        }
    }
}

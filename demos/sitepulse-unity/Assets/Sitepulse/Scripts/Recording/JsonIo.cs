// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Globalization;
using System.Text.Json;

namespace DeviceChain.Sitepulse.Recording
{
    /// <summary>
    /// Hand reading of <see cref="JsonElement"/>s, and the one spelling of a time. Nothing here uses reflection, so it runs
    /// the same in the Editor and in an IL2CPP player.
    /// </summary>
    public static class JsonIo
    {
        public static string Time(DateTimeOffset t) => t.UtcDateTime.ToString("O", CultureInfo.InvariantCulture);

        public static DateTimeOffset ParseTime(string s, string what)
        {
            if (!DateTimeOffset.TryParse(s, CultureInfo.InvariantCulture, DateTimeStyles.AssumeUniversal | DateTimeStyles.AdjustToUniversal, out var t))
                throw new RecordingFormatException($"{what}: \"{s}\" is not a time");
            return t;
        }

        public static string Str(JsonElement e, string name, string fallback = null) =>
            e.TryGetProperty(name, out var v) && v.ValueKind == JsonValueKind.String ? v.GetString() : fallback;

        public static string RequireStr(JsonElement e, string name, string where)
        {
            var s = Str(e, name);
            return s ?? throw new RecordingFormatException($"{where}: \"{name}\" is missing");
        }

        public static double Num(JsonElement e, string name, double fallback = 0.0) =>
            e.TryGetProperty(name, out var v) && v.ValueKind == JsonValueKind.Number ? v.GetDouble() : fallback;

        public static double? NumOrNull(JsonElement e, string name) =>
            e.TryGetProperty(name, out var v) && v.ValueKind == JsonValueKind.Number ? v.GetDouble() : (double?)null;

        public static long Long(JsonElement e, string name, long fallback = 0) =>
            e.TryGetProperty(name, out var v) && v.ValueKind == JsonValueKind.Number ? v.GetInt64() : fallback;

        public static bool Bool(JsonElement e, string name, bool fallback = false) =>
            e.TryGetProperty(name, out var v) && (v.ValueKind == JsonValueKind.True || v.ValueKind == JsonValueKind.False) ? v.GetBoolean() : fallback;

        public static DateTimeOffset? TimeOrNull(JsonElement e, string name, string where)
        {
            var s = Str(e, name);
            return s == null ? (DateTimeOffset?)null : ParseTime(s, where + "." + name);
        }

        public static void WriteTime(Utf8JsonWriter w, string name, DateTimeOffset? t)
        {
            if (t.HasValue) w.WriteString(name, Time(t.Value));
        }

        public static void WriteStr(Utf8JsonWriter w, string name, string s)
        {
            if (s != null) w.WriteString(name, s);
        }

        public static void WriteNum(Utf8JsonWriter w, string name, double? v)
        {
            if (v.HasValue && !double.IsNaN(v.Value) && !double.IsInfinity(v.Value)) w.WriteNumber(name, v.Value);
        }

        /// <summary>Seconds, written to the millisecond: a recording does not need more, and the lines stay short.</summary>
        public static double Seconds(double t) => Math.Round(t, 3);
    }
}

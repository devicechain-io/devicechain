// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Text;
using System.Text.Json;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>
    /// The observer's GraphQL documents, their variables (written by hand, no reflection serializer)
    /// and the pure parse of each answer into <see cref="ObserverItem"/>s. Field names are the
    /// platform's schema exactly: a command is named by <c>name</c>, a device by <c>deviceToken</c>.
    /// </summary>
    public static class ObserverQueries
    {
        public const int AlarmPage = 100;

        /// <summary>The most pages of active alarms one snapshot reads before it gives up and says it is truncated.</summary>
        public const int MaxAlarmPages = 10;
        public const int CommandPage = 100;

        public const string MeasurementSubscription = "subscription { measurementStream { deviceToken name value occurredTime } }";

        public const string AlarmSubscription =
            "subscription { alarmStream { eventType alarmToken originatorType originatorToken alarmKey metricKey state severity previousSeverity acknowledged lastValue raisedTime occurredTime } }";

        public const string LocationsQuery =
            "query Locations($t: [String!]!) { latestLocations(deviceTokens: $t) { deviceToken speed heading elevation occurredTime } }";

        public const string PresenceQuery =
            "query Presence($t: [String!]!) { deviceStatesByDeviceToken(deviceTokens: $t) { deviceToken active lastActivityTime } }";

        public const string ActiveAlarmsQuery =
            "query Alarms($c: AlarmSearchCriteria!) { alarms(criteria: $c) { results { token originatorType originatorToken alarmKey metricKey state severity acknowledged raisedTime acknowledgedTime clearedTime } pagination { totalRecords } } }";

        const string CommandFields = "token deviceToken name status queuedTime";

        /// <summary>The active commands' page, and the named commands too when <paramref name="withTokens"/>.</summary>
        public static string CommandsQuery(bool withTokens) => withTokens
            ? "query Commands($c: CommandSearchCriteria!, $t: [String!]!) { commands(criteria: $c) { results { " + CommandFields + " } } commandsByToken(tokens: $t) { " + CommandFields + " } }"
            : "query Commands($c: CommandSearchCriteria!) { commands(criteria: $c) { results { " + CommandFields + " } } }";

        public static string MeasurementsSnapshotQuery(IReadOnlyList<string> deviceTokens)
        {
            var sb = new StringBuilder("query Snapshot(");
            for (var i = 0; i < deviceTokens.Count; i++) sb.Append(i == 0 ? "" : ", ").Append("$t").Append(i).Append(": String!");
            sb.Append(") {");
            for (var i = 0; i < deviceTokens.Count; i++)
                sb.Append(" d").Append(i).Append(": latestMeasurements(deviceToken: $t").Append(i).Append(") { name value occurredTime }");
            return sb.Append(" }").ToString();
        }

        // ---------------------------------------------------------------- variables
        public const string NoVariables = "{}";

        public static string MeasurementsSnapshotVariables(IReadOnlyList<string> deviceTokens) => LastStateQuery.Variables(deviceTokens);

        public static string TokenListVariables(IEnumerable<string> tokens) => VarsJson.StringList("t", tokens);

        public static string ActiveAlarmsVariables(int page = 1) => Write(w =>
        {
            w.WriteStartObject("c");
            w.WriteNumber("pageNumber", page);
            w.WriteNumber("pageSize", AlarmPage);
            w.WriteString("state", ObservedAlarm.Active);
            w.WriteEndObject();
        });

        /// <summary>The non-terminal commands' page; the named tokens when there are any.</summary>
        public static string CommandsVariables(IReadOnlyCollection<string> pendingTokens) => Write(w =>
        {
            w.WriteStartObject("c");
            w.WriteNumber("pageNumber", 1);
            w.WriteNumber("pageSize", CommandPage);
            w.WriteStartArray("statuses");
            foreach (var s in new[] { "QUEUED", "HELD", "SENT", "PARKED" }) w.WriteStringValue(s);
            w.WriteEndArray();
            w.WriteEndObject();
            if (pendingTokens != null && pendingTokens.Count > 0)
            {
                w.WriteStartArray("t");
                foreach (var t in pendingTokens) w.WriteStringValue(t);
                w.WriteEndArray();
            }
        });

        static string Write(Action<Utf8JsonWriter> body)
        {
            using var ms = new MemoryStream();
            using (var w = new Utf8JsonWriter(ms))
            {
                w.WriteStartObject();
                body(w);
                w.WriteEndObject();
            }

            return Encoding.UTF8.GetString(ms.ToArray());
        }

        // ---------------------------------------------------------------- endpoints
        /// <summary>
        /// The alarm stream's WebSocket address: the runner's event-management address with the
        /// device-management area in its place. An address that is not that shape is refused rather
        /// than guessed at.
        /// </summary>
        public static Uri AlarmSocket(Uri measurementSocket)
        {
            const string From = "/api/event-management/graphql";
            if (measurementSocket == null || !measurementSocket.AbsolutePath.EndsWith(From, StringComparison.Ordinal))
                throw new ArgumentException($"the runner's wsUrl is not an event-management address ({From}), so the alarm stream's address cannot be derived from it", nameof(measurementSocket));
            var b = new UriBuilder(measurementSocket);
            b.Path = b.Path.Substring(0, b.Path.Length - From.Length) + "/api/device-management/graphql";
            return b.Uri;
        }

        // ---------------------------------------------------------------- parse
        /// <summary>A platform timestamp (RFC 3339, UTC) or null.</summary>
        public static DateTimeOffset? Time(JsonElement o, string name)
        {
            if (o.ValueKind != JsonValueKind.Object || !o.TryGetProperty(name, out var v) || v.ValueKind != JsonValueKind.String) return null;
            return DateTimeOffset.TryParse(v.GetString(), CultureInfo.InvariantCulture, DateTimeStyles.AssumeUniversal | DateTimeStyles.AdjustToUniversal, out var t) ? t : (DateTimeOffset?)null;
        }

        static double? Num(JsonElement o, string name) =>
            o.TryGetProperty(name, out var v) && v.ValueKind == JsonValueKind.Number && v.TryGetDouble(out var d) ? d : (double?)null;

        static string Str(JsonElement o, string name) => DeviceBinder.Str(o, name);

        /// <summary>One <c>measurementStream</c> frame. A frame with no device, name, value or time says nothing and gives null.</summary>
        public static MeasurementItem ParseMeasurementFrame(JsonElement frame, DateTimeOffset observedAt)
        {
            if (!frame.TryGetProperty("measurementStream", out var m) || m.ValueKind != JsonValueKind.Object) return null;
            var token = Str(m, "deviceToken");
            var name = Str(m, "name");
            var value = Num(m, "value");
            var at = Time(m, "occurredTime");
            if (token == null || name == null || !value.HasValue || !at.HasValue) return null;
            return new MeasurementItem(token, name, value.Value, at.Value, observedAt, false);
        }

        /// <summary>The aliased <c>latestMeasurements</c> answer for <paramref name="deviceTokens"/>.</summary>
        public static List<MeasurementItem> ParseMeasurementsSnapshot(IReadOnlyList<string> deviceTokens, string dataJson, DateTimeOffset observedAt)
        {
            var items = new List<MeasurementItem>();
            using var doc = JsonDocument.Parse(dataJson);
            for (var i = 0; i < deviceTokens.Count; i++)
            {
                if (!doc.RootElement.TryGetProperty("d" + i, out var rows) || rows.ValueKind != JsonValueKind.Array) continue;
                foreach (var row in rows.EnumerateArray())
                {
                    var name = Str(row, "name");
                    var value = Num(row, "value");
                    var at = Time(row, "occurredTime");
                    if (name == null || !value.HasValue || !at.HasValue) continue;
                    items.Add(new MeasurementItem(deviceTokens[i], name, value.Value, at.Value, observedAt, true));
                }
            }

            return items;
        }

        public static List<LocationItem> ParseLocations(string dataJson, DateTimeOffset observedAt)
        {
            var items = new List<LocationItem>();
            using var doc = JsonDocument.Parse(dataJson);
            if (!doc.RootElement.TryGetProperty("latestLocations", out var rows) || rows.ValueKind != JsonValueKind.Array) return items;
            foreach (var row in rows.EnumerateArray())
            {
                var token = Str(row, "deviceToken");
                var at = Time(row, "occurredTime");
                if (token == null || !at.HasValue) continue;
                items.Add(new LocationItem(token, new ObservedLocation
                {
                    SpeedMps = Num(row, "speed"),
                    HeadingDegrees = Num(row, "heading"),
                    ElevationMetres = Num(row, "elevation"),
                    OccurredAt = at.Value,
                    ObservedAt = observedAt,
                }));
            }

            return items;
        }

        public static List<PresenceItem> ParsePresence(string dataJson, DateTimeOffset observedAt)
        {
            var items = new List<PresenceItem>();
            using var doc = JsonDocument.Parse(dataJson);
            if (!doc.RootElement.TryGetProperty("deviceStatesByDeviceToken", out var rows) || rows.ValueKind != JsonValueKind.Array) return items;
            foreach (var row in rows.EnumerateArray())
            {
                var token = Str(row, "deviceToken");
                if (token == null || !row.TryGetProperty("active", out var a) || (a.ValueKind != JsonValueKind.True && a.ValueKind != JsonValueKind.False)) continue;
                items.Add(new PresenceItem(token, new ObservedPresence { Active = a.GetBoolean(), LastActivityAt = Time(row, "lastActivityTime"), ObservedAt = observedAt }));
            }

            return items;
        }

        /// <summary>An alarm from the active snapshot (<c>raisedTime</c> stands for its time) or a stream frame (<c>occurredTime</c>). Null when it names no device.</summary>
        public static AlarmItem ParseAlarm(JsonElement o, DateTimeOffset observedAt)
        {
            var device = Str(o, "originatorToken");
            var token = Str(o, "token") ?? Str(o, "alarmToken");
            var key = Str(o, "alarmKey");
            if (device == null || token == null || key == null) return null;
            // a stream frame says when the transition happened (occurredTime). A snapshot row has no such field:
            // its newest transition is the latest of when it was raised, acknowledged and cleared
            var at = Time(o, "occurredTime") ?? Newest(Time(o, "raisedTime"), Time(o, "acknowledgedTime"), Time(o, "clearedTime"));
            if (!at.HasValue) return null;
            return new AlarmItem(device, new ObservedAlarm
            {
                Token = token,
                AlarmKey = key,
                MetricKey = Str(o, "metricKey"),
                State = Str(o, "state") ?? "",
                Severity = Str(o, "severity"),
                Acknowledged = o.TryGetProperty("acknowledged", out var a) && a.ValueKind == JsonValueKind.True,
                OccurredAt = at.Value,
                ObservedAt = observedAt,
            });
        }

        static DateTimeOffset? Newest(params DateTimeOffset?[] times)
        {
            DateTimeOffset? newest = null;
            foreach (var t in times)
                if (t.HasValue && (!newest.HasValue || t.Value > newest.Value)) newest = t;
            return newest;
        }

        public static AlarmItem ParseAlarmFrame(JsonElement frame, DateTimeOffset observedAt) =>
            frame.TryGetProperty("alarmStream", out var a) && a.ValueKind == JsonValueKind.Object ? ParseAlarm(a, observedAt) : null;

        /// <summary>One page of the active-alarm answer: the alarms that name a device, how many rows the page held, and the platform's total.</summary>
        public sealed class AlarmPageResult
        {
            public List<AlarmItem> Alarms { get; } = new List<AlarmItem>();
            public int Rows { get; internal set; }
            public int? TotalRecords { get; internal set; }
        }

        public static AlarmPageResult ParseActiveAlarmsPage(string dataJson, DateTimeOffset observedAt)
        {
            var page = new AlarmPageResult();
            using var doc = JsonDocument.Parse(dataJson);
            if (!doc.RootElement.TryGetProperty("alarms", out var results)) return page;
            if (results.TryGetProperty("pagination", out var pg) && pg.ValueKind == JsonValueKind.Object)
            {
                var total = Num(pg, "totalRecords");
                if (total.HasValue) page.TotalRecords = (int)total.Value;
            }

            if (results.TryGetProperty("results", out var rows) && rows.ValueKind == JsonValueKind.Array)
                foreach (var row in rows.EnumerateArray())
                {
                    page.Rows++;
                    var a = ParseAlarm(row, observedAt);
                    if (a != null) page.Alarms.Add(a);
                }

            return page;
        }

        /// <summary>The active-alarm answer, one page: truncated when the platform says it has more than this page held.</summary>
        public static AlarmSnapshotItem ParseActiveAlarms(string dataJson, DateTimeOffset requestedAt, DateTimeOffset observedAt)
        {
            var page = ParseActiveAlarmsPage(dataJson, observedAt);
            return new AlarmSnapshotItem(page.Alarms, requestedAt, observedAt, MoreRemain(page.Rows, page.Rows, page.TotalRecords), page.TotalRecords);
        }

        // whether the platform holds alarms this read has not seen: by its total when it gave one, else by a full page
        static bool MoreRemain(int fetched, int lastPageRows, int? total) => total.HasValue ? fetched < total.Value : lastPageRows >= AlarmPage;

        /// <summary>
        /// The whole active-alarm list: <paramref name="fetchPage"/> (given a 1-based page number) is asked
        /// for pages until the platform's total is reached, up to <see cref="MaxAlarmPages"/>. If alarms
        /// remain unread at the end the result is marked truncated, and must not be used to clear anything.
        /// </summary>
        public static async System.Threading.Tasks.Task<AlarmSnapshotItem> FetchActiveAlarms(
            Func<int, System.Threading.Tasks.Task<string>> fetchPage, DateTimeOffset requestedAt, Func<DateTimeOffset> clock, int maxPages = MaxAlarmPages)
        {
            var all = new List<AlarmItem>();
            var fetched = 0;
            int? total = null;
            var more = true;
            for (var p = 1; p <= maxPages && more; p++)
            {
                var data = await fetchPage(p).ConfigureAwait(true);
                var page = ParseActiveAlarmsPage(data, clock());
                all.AddRange(page.Alarms);
                fetched += page.Rows;
                total = page.TotalRecords ?? total;
                more = page.Rows > 0 && MoreRemain(fetched, page.Rows, total);
                // a page that came back empty while the platform says it has more cannot be made to progress
                if (page.Rows == 0 && total.HasValue && fetched < total.Value) return new AlarmSnapshotItem(all, requestedAt, clock(), true, total);
            }

            return new AlarmSnapshotItem(all, requestedAt, clock(), more, total);
        }

        /// <summary>The commands of a <see cref="CommandsQuery"/> answer: the page, then the named ones, de-duplicated by token (the named copy is the later word).</summary>
        public static List<CommandItem> ParseCommands(string dataJson, DateTimeOffset observedAt)
        {
            var byToken = new Dictionary<string, CommandItem>(StringComparer.Ordinal);
            var order = new List<string>();
            using var doc = JsonDocument.Parse(dataJson);
            void Add(JsonElement row)
            {
                var token = Str(row, "token");
                var device = Str(row, "deviceToken");
                var name = Str(row, "name");
                var status = Str(row, "status");
                var queued = Time(row, "queuedTime");
                if (token == null || device == null || name == null || status == null) return;
                if (!byToken.ContainsKey(token)) order.Add(token);
                byToken[token] = new CommandItem(device, new ObservedCommand { Token = token, Name = name, Status = status, QueuedAt = queued ?? DateTimeOffset.MinValue, ObservedAt = observedAt });
            }

            if (doc.RootElement.TryGetProperty("commands", out var page) && page.TryGetProperty("results", out var rows) && rows.ValueKind == JsonValueKind.Array)
                foreach (var row in rows.EnumerateArray()) Add(row);
            if (doc.RootElement.TryGetProperty("commandsByToken", out var named) && named.ValueKind == JsonValueKind.Array)
                foreach (var row in named.EnumerateArray()) Add(row);
            var items = new List<CommandItem>(order.Count);
            foreach (var t in order) items.Add(byToken[t]);
            return items;
        }
    }
}

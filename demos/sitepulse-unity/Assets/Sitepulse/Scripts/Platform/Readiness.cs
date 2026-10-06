// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>
    /// How far a device has got: bound, credentialed, then its MQTT session connecting, ready (the
    /// start returned: connected and subscribed) and publishing (the broker acknowledged a sample).
    /// Observed is the top rung: the platform itself reported a measurement the device published in this
    /// run, which is the only evidence that the telemetry arrived. A failure is a side state, not a stage.
    /// </summary>
    public enum DeviceStage { Unbound, Resolved, Credentialed, Connecting, Ready, Publishing, Observed }

    /// <summary>
    /// What a session is doing besides climbing the ladder. Blind is terminal (the broker refused the
    /// device) and is also a failure; Reconnecting is temporary; Stopped is a session that was
    /// disposed.
    /// </summary>
    public enum DeviceSide { None, Reconnecting, Blind, Stopped }

    public enum LineKind { Pending, Ok, Failed }

    public readonly struct PanelLine
    {
        public PanelLine(string text, LineKind kind)
        {
            Text = text;
            Kind = kind;
        }

        public string Text { get; }
        public LineKind Kind { get; }
    }

    public sealed class DeviceReadiness
    {
        public SceneDevice Device { get; set; }
        public DeviceStage Stage { get; set; } = DeviceStage.Unbound;

        /// <summary>BindFailed: why the device cannot proceed; null while it can.</summary>
        public string FailReason { get; set; }

        public BindResult Bind { get; set; }
        public CredentialPath? Path { get; set; }
        public DeviceSide Side { get; set; }

        /// <summary>When the broker last acknowledged a sample, UTC; null until one has.</summary>
        public DateTimeOffset? LastPublishUtc { get; set; }

        public long Published { get; set; }
        public long SendErrors { get; set; }

        /// <summary>Samples the bounded outbound ring discarded to make room.</summary>
        public long Dropped { get; set; }

        /// <summary>The last command the device was sent and what it answered; null if none.</summary>
        public string LastCommand { get; set; }

        /// <summary>Consecutive failed sends since the last acknowledged one.</summary>
        public int ConsecutiveSendFailures { get; set; }

        /// <summary>
        /// Why a device with a session is not really publishing right now (no acknowledgement for a while,
        /// or sends failing); null while it is. Set by <see cref="ReadinessBoard.Evaluate"/>. A stalled
        /// device is neither publishing nor failed: it has a session and may recover.
        /// </summary>
        public string StallReason { get; set; }

        public bool Stalled => StallReason != null;

        /// <summary>
        /// An observed device whose newest own-run measurement is older than <see cref="ReadinessBoard.ObservedWithin"/>
        /// (or that has none): the platform reported it once and has not lately. Set by
        /// <see cref="ReadinessBoard.EvaluateObserved"/>.
        /// </summary>
        public bool Quiet { get; set; }

        /// <summary>When the device's newest own-run measurement happened; null if none was seen.</summary>
        public DateTimeOffset? LastMeasurementAt { get; set; }

        public bool Failed => FailReason != null;

        /// <summary>
        /// Drawn as a grey placeholder: it has no working session, now or later (it did not bind, has no
        /// credential, could not create or start a session, or the broker refused it). A reconnecting,
        /// connecting or stalled device has a session and is not grey.
        /// </summary>
        public bool IsGrey => Failed;
    }

    /// <summary>
    /// The per-device readiness the panel shows. Counts are exact: <c>n/19 credentialed · k failed</c>
    /// (once sessions are starting, <c>n/19 publishing · k failed</c>; once the observer runs,
    /// <c>n/19 observed · k failed</c>) counts devices at that state and never rounds up, and a device is one or the other, so pending devices are in
    /// neither number. A device that is reconnecting is neither publishing nor failed, and one that has a
    /// session but has gone quiet (<see cref="Evaluate"/>) is stalled: not publishing, not failed.
    /// </summary>
    public sealed class ReadinessBoard
    {
        readonly List<DeviceReadiness> devices = new List<DeviceReadiness>();
        readonly Dictionary<string, DeviceReadiness> byId = new Dictionary<string, DeviceReadiness>(StringComparer.Ordinal);
        readonly string tenant;

        public ReadinessBoard(IReadOnlyList<SceneDevice> scene, string tenant)
        {
            this.tenant = tenant;
            foreach (var d in scene)
            {
                var r = new DeviceReadiness { Device = d };
                devices.Add(r);
                byId[d.ExternalId] = r;
            }
        }

        /// <summary>Bumped on every change, so a view redraws only when something moved.</summary>
        public int Version { get; private set; }

        public IReadOnlyList<DeviceReadiness> Devices => devices;
        public int Total => devices.Count;

        public int CredentialedCount
        {
            get
            {
                var n = 0;
                foreach (var d in devices)
                    if (d.Stage == DeviceStage.Credentialed && !d.Failed) n++;
                return n;
            }
        }

        /// <summary>Longest a publishing device may go without an acknowledgement before it is called stalled.</summary>
        public static readonly TimeSpan StallAfter = TimeSpan.FromSeconds(5);

        /// <summary>Consecutive failed sends at which a device is called stalled.</summary>
        public const int StallAfterFailures = 3;

        /// <summary>
        /// Devices whose session has acknowledged a sample within <see cref="StallAfter"/>, with fewer than
        /// <see cref="StallAfterFailures"/> failed sends in a row, and that are not reconnecting, stopped or failed.
        /// </summary>
        public int PublishingCount
        {
            get
            {
                var n = 0;
                foreach (var d in devices)
                    if (d.Stage == DeviceStage.Publishing && d.Side == DeviceSide.None && !d.Failed && !d.Stalled) n++;
                return n;
            }
        }

        /// <summary>
        /// Devices whose telemetry the platform reported back in this run, and that are still sending
        /// (not stalled, reconnecting, stopped or failed).
        /// </summary>
        public int ObservedCount
        {
            get
            {
                var n = 0;
                foreach (var d in devices)
                    if (IsObserved(d)) n++;
                return n;
            }
        }

        static bool IsObserved(DeviceReadiness d) =>
            d.Stage == DeviceStage.Observed && d.Side == DeviceSide.None && !d.Failed && !d.Stalled && !d.Quiet;

        /// <summary>Observed once, but with no measurement from this run in the last <see cref="ObservedWithin"/> (and otherwise sending).</summary>
        public int QuietCount
        {
            get
            {
                var n = 0;
                foreach (var d in devices)
                    if (d.Stage == DeviceStage.Observed && d.Side == DeviceSide.None && !d.Failed && !d.Stalled && d.Quiet) n++;
                return n;
            }
        }

        /// <summary>
        /// A device counts as observed only while its newest measurement from this run is no older than this:
        /// the same 15 s at which a value on a card stops being merely stale and goes grey.
        /// </summary>
        public static readonly TimeSpan ObservedWithin = TimeSpan.FromSeconds(15);

        public int StalledCount
        {
            get
            {
                var n = 0;
                foreach (var d in devices)
                    if (d.Stage >= DeviceStage.Publishing && d.Side == DeviceSide.None && !d.Failed && d.Stalled) n++;
                return n;
            }
        }

        /// <summary>True once the device plane has begun starting sessions; the header then counts publishing, not credentialed.</summary>
        public bool SessionsBegun { get; private set; }

        public int FailedCount
        {
            get
            {
                var n = 0;
                foreach (var d in devices)
                    if (d.Failed) n++;
                return n;
            }
        }

        public DeviceReadiness this[string externalId] => byId[externalId];

        /// <summary>The platform token a scene device bound to; null when it is unknown or did not bind.</summary>
        public string TokenOf(string externalId) =>
            byId.TryGetValue(externalId ?? "", out var d) && d.Bind != null && d.Bind.IsBound ? d.Bind.DeviceToken : null;

        /// <summary>True once the observer is running; the header then counts observed, not publishing.</summary>
        public bool ObserverBegun { get; private set; }

        public string Summary() => ObserverBegun
            ? $"{ObservedCount}/{Total} observed{(StalledCount > 0 ? $" · {StalledCount} stalled" : "")}{(QuietCount > 0 ? $" · {QuietCount} quiet" : "")} · {FailedCount} failed"
            : SessionsBegun
                ? $"{PublishingCount}/{Total} publishing{(StalledCount > 0 ? $" · {StalledCount} stalled" : "")} · {FailedCount} failed"
                : $"{CredentialedCount}/{Total} credentialed · {FailedCount} failed";

        public void BeginObserver()
        {
            if (ObserverBegun) return;
            ObserverBegun = true;
            Version++;
        }

        /// <summary>
        /// The platform reported a measurement this device published in this run. Raises the device to
        /// Observed (the board may not yet have heard of the first acknowledgement, which can trail the
        /// platform's report). A device that failed, or has no session at all, is left where it is: a
        /// value cannot be the platform's report of a device that never sent one.
        /// </summary>
        public bool MarkObserved(string deviceToken)
        {
            foreach (var d in devices)
            {
                if (d.Bind == null || d.Bind.DeviceToken != deviceToken) continue;
                if (d.Failed || d.Stage < DeviceStage.Connecting || d.Stage >= DeviceStage.Observed) return false;
                d.Stage = DeviceStage.Observed;
                Version++;
                return true;
            }

            return false;
        }

        /// <summary>
        /// Re-judges, at <paramref name="now"/>, which observed devices have gone quiet: <paramref name="newestOwnRunOf"/>
        /// gives a device token's newest own-run measurement time. Observed is a stage reached once; being counted as
        /// observed lasts only while the platform keeps reporting.
        /// </summary>
        public void EvaluateObserved(DateTimeOffset now, Func<string, DateTimeOffset?> newestOwnRunOf)
        {
            foreach (var d in devices)
            {
                var quiet = false;
                DateTimeOffset? last = null;
                if (d.Stage == DeviceStage.Observed && d.Bind != null && d.Bind.IsBound)
                {
                    last = newestOwnRunOf(d.Bind.DeviceToken);
                    quiet = !last.HasValue || now - last.Value > ObservedWithin;
                }

                if (quiet == d.Quiet && last == d.LastMeasurementAt) continue;
                d.Quiet = quiet;
                d.LastMeasurementAt = last;
                Version++;
            }
        }

        /// <summary>Marks every credentialed device as connecting, and switches the header to the publishing count.</summary>
        public void BeginSessions()
        {
            SessionsBegun = true;
            foreach (var d in devices)
                if (d.Stage == DeviceStage.Credentialed && !d.Failed) d.Stage = DeviceStage.Connecting;
            Version++;
        }

        public void SetStage(string externalId, DeviceStage stage)
        {
            var d = byId[externalId];
            if (d.Stage == stage) return;
            d.Stage = stage;
            Version++;
        }

        public void SetSide(string externalId, DeviceSide side)
        {
            var d = byId[externalId];
            if (d.Side == side) return;
            d.Side = side;
            Version++;
        }

        /// <summary>A session-level failure: the device has no working session, now or later.</summary>
        public void FailSession(string externalId, string reason)
        {
            var d = byId[externalId];
            d.FailReason = $"{externalId} · {reason}";
            Version++;
        }

        public void SetCommand(string externalId, string note)
        {
            byId[externalId].LastCommand = note;
            Version++;
        }

        /// <summary>Copies a session's counters in; a redraw is asked for only when one moved.</summary>
        public void SetStats(string externalId, long published, long sendErrors, long dropped, DateTimeOffset? lastPublishUtc, int consecutiveFailures = 0)
        {
            var d = byId[externalId];
            if (d.Published == published && d.SendErrors == sendErrors && d.Dropped == dropped && d.LastPublishUtc == lastPublishUtc
                && d.ConsecutiveSendFailures == consecutiveFailures) return;
            d.Published = published;
            d.SendErrors = sendErrors;
            d.Dropped = dropped;
            d.LastPublishUtc = lastPublishUtc;
            d.ConsecutiveSendFailures = consecutiveFailures;
            Version++;
        }

        /// <summary>
        /// Decides, at <paramref name="now"/>, which publishing devices have stalled: no acknowledgement
        /// within <see cref="StallAfter"/>, or <see cref="StallAfterFailures"/> failed sends in a row. A
        /// device that has never published is not stalled (it is still on the ladder).
        /// </summary>
        public void Evaluate(DateTimeOffset now)
        {
            foreach (var d in devices)
            {
                string reason = null;
                if (d.Stage >= DeviceStage.Publishing && d.Side == DeviceSide.None && !d.Failed)
                {
                    if (d.ConsecutiveSendFailures >= StallAfterFailures)
                        reason = $"sends failing · {d.ConsecutiveSendFailures} in a row";
                    else if (!d.LastPublishUtc.HasValue || now - d.LastPublishUtc.Value > StallAfter)
                    {
                        var quiet = d.LastPublishUtc.HasValue ? (int)(now - d.LastPublishUtc.Value).TotalSeconds : 0;
                        reason = $"no acknowledgement for {quiet} s";
                    }
                }

                if (reason == d.StallReason) continue;
                d.StallReason = reason;
                Version++;
            }
        }

        /// <summary>
        /// The devices that should be drawn grey and are not yet in <paramref name="already"/>, which it
        /// then records. Grey is applied once per machine and never lifted.
        /// </summary>
        public List<SceneDevice> TakeNewlyGrey(ISet<string> already)
        {
            var list = new List<SceneDevice>();
            foreach (var d in devices)
                if (d.IsGrey && already.Add(d.Device.ExternalId)) list.Add(d.Device);
            return list;
        }

        public void SetBind(BindResult r)
        {
            var d = byId[r.ExternalId];
            d.Bind = r;
            if (r.IsBound)
            {
                d.Stage = DeviceStage.Resolved;
                d.FailReason = null;
            }
            else
            {
                d.Stage = DeviceStage.Unbound;
                d.FailReason = r.Describe(tenant);
            }

            Version++;
        }

        public void FailBind(string externalId, string reason)
        {
            var d = byId[externalId];
            d.Stage = DeviceStage.Unbound;
            d.FailReason = $"{externalId} · {reason}";
            Version++;
        }

        public void SetCredential(CredentialOutcome o)
        {
            var d = byId[o.ExternalId];
            d.Path = o.Path;
            if (o.Ok)
            {
                d.Stage = DeviceStage.Credentialed;
                d.FailReason = null;
            }
            else
            {
                d.FailReason = $"{o.ExternalId} · no credential · {o.Reason}";
            }

            Version++;
        }

        public void FailCredential(string externalId, string reason)
        {
            byId[externalId].FailReason = $"{externalId} · no credential · {reason}";
            Version++;
        }

        public IReadOnlyList<PanelLine> Lines()
        {
            var lines = new List<PanelLine>(devices.Count);
            foreach (var d in devices)
            {
                var id = d.Device.ExternalId;
                if (d.Failed) lines.Add(new PanelLine(d.FailReason + Command(d), LineKind.Failed));
                else if (d.Stage >= DeviceStage.Connecting) lines.Add(SessionLine(d));
                else if (d.Stage == DeviceStage.Credentialed)
                    lines.Add(new PanelLine($"{id} · credentialed · {d.Bind.DeviceToken} · {d.Path.ToString().ToLowerInvariant()}", LineKind.Ok));
                else if (d.Stage == DeviceStage.Resolved)
                    lines.Add(new PanelLine($"{id} · resolved · {d.Bind.DeviceToken} · credential pending", LineKind.Pending));
                else
                    lines.Add(new PanelLine($"{id} · resolving", LineKind.Pending));
            }

            return lines;
        }

        static string Command(DeviceReadiness d) => d.LastCommand == null ? "" : " · " + d.LastCommand;

        static PanelLine SessionLine(DeviceReadiness d)
        {
            var id = d.Device.ExternalId;
            string state;
            switch (d.Side)
            {
                case DeviceSide.Reconnecting: state = "reconnecting"; break;
                case DeviceSide.Stopped: state = "stopped"; break;
                default:
                    state = d.Stage == DeviceStage.Connecting ? "connecting" : d.Stage == DeviceStage.Ready ? "ready"
                        : d.Stalled ? "stalled · " + d.StallReason
                        : d.Stage == DeviceStage.Observed ? (d.Quiet ? "quiet · " + (d.LastMeasurementAt.HasValue ? "last measurement " + d.LastMeasurementAt.Value.UtcDateTime.ToString("HH:mm:ss") + "Z" : "no measurement") : "observed")
                        : "publishing";
                    break;
            }

            var text = $"{id} · {state}";
            if (d.Stage >= DeviceStage.Publishing || d.Published > 0)
            {
                text += $" · {d.Published} sent";
                if (d.LastPublishUtc.HasValue) text += $" · last {d.LastPublishUtc.Value.UtcDateTime:HH:mm:ss}Z";
            }

            if (d.SendErrors > 0) text += $" · {d.SendErrors} send errors";
            if (d.Dropped > 0) text += $" · {d.Dropped} dropped";
            text += Command(d);
            var ok = d.Stage >= DeviceStage.Publishing && d.Side == DeviceSide.None && !d.Stalled && !d.Quiet;
            return new PanelLine(text, ok ? LineKind.Ok : LineKind.Pending);
        }

        /// <summary>
        /// One line for the compact panel: either that every device is where the header's count says it
        /// should be, or which ones are not and why ("SP-LD-0004 stalled · SP-DZ-0002 reconnecting"), the
        /// first <paramref name="maxNamed"/> of them and a count of the rest.
        /// </summary>
        public string Brief(int maxNamed = 4)
        {
            var parts = new List<string>();
            foreach (var d in devices)
            {
                var why = Problem(d);
                if (why != null) parts.Add(d.Device.ExternalId + " " + why);
            }

            if (parts.Count == 0) return ObserverBegun ? "all observed" : SessionsBegun ? "all publishing" : "all credentialed";
            var shown = parts.Count > maxNamed ? parts.GetRange(0, maxNamed) : parts;
            var text = string.Join(" · ", shown);
            return parts.Count > maxNamed ? text + " · +" + (parts.Count - maxNamed) + " more" : text;
        }

        // why a device is not where the current phase wants it; null when it is
        string Problem(DeviceReadiness d)
        {
            if (d.Failed) return "failed";
            switch (d.Side)
            {
                case DeviceSide.Reconnecting: return "reconnecting";
                case DeviceSide.Blind: return "blind";
                case DeviceSide.Stopped: return "stopped";
            }

            if (ObserverBegun)
            {
                if (IsObserved(d)) return null;
                if (d.Stalled) return "stalled";
                if (d.Stage == DeviceStage.Observed) return "quiet";
            }
            else if (SessionsBegun)
            {
                if (d.Stage >= DeviceStage.Publishing && !d.Stalled) return null;
                if (d.Stalled) return "stalled";
            }
            else if (d.Stage >= DeviceStage.Credentialed) return null;

            switch (d.Stage)
            {
                case DeviceStage.Unbound: return "resolving";
                case DeviceStage.Resolved: return "awaiting credential";
                case DeviceStage.Credentialed: return "credentialed";
                case DeviceStage.Connecting: return "connecting";
                case DeviceStage.Ready: return "ready";
                default: return "awaiting observation";
            }
        }

        /// <summary>Things worth saying once about the fleet, not about one device.</summary>
        public IReadOnlyList<string> Notes()
        {
            var byArea = new SortedDictionary<string, List<string>>(StringComparer.Ordinal);
            foreach (var d in devices)
            {
                if (d.Bind == null) continue;
                foreach (var area in d.Bind.UnmappedAreas)
                {
                    if (!byArea.TryGetValue(area, out var who)) byArea[area] = who = new List<string>();
                    who.Add(d.Device.ExternalId);
                }
            }

            var notes = new List<string>();
            foreach (var kv in byArea)
                notes.Add($"goto-area accepts \"{kv.Key}\", which has no geometry in this scene ({kv.Value.Count} devices); the command is refused at run time");
            return notes;
        }
    }
}

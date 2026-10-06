// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>
    /// How far a device has got: bound, credentialed, then its MQTT session connecting, ready (the
    /// start returned: connected and subscribed) and publishing (the broker acknowledged a sample).
    /// Observation by the platform extends the ladder later and is not claimed here. A failure is a
    /// side state, not a stage.
    /// </summary>
    public enum DeviceStage { Unbound, Resolved, Credentialed, Connecting, Ready, Publishing }

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

        public bool Failed => FailReason != null;
    }

    /// <summary>
    /// The per-device readiness the panel shows. Counts are exact: <c>n/19 credentialed · k failed</c>
    /// (and, once sessions are starting, <c>n/19 publishing · k failed</c>) counts devices at that
    /// state and never rounds up, and a device is one or the other, so pending devices are in
    /// neither number. A device that is reconnecting is neither publishing nor failed.
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

        /// <summary>Devices whose session has acknowledged a sample and that are not currently reconnecting, stopped or failed.</summary>
        public int PublishingCount
        {
            get
            {
                var n = 0;
                foreach (var d in devices)
                    if (d.Stage == DeviceStage.Publishing && d.Side == DeviceSide.None && !d.Failed) n++;
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

        public string Summary() => SessionsBegun
            ? $"{PublishingCount}/{Total} publishing · {FailedCount} failed"
            : $"{CredentialedCount}/{Total} credentialed · {FailedCount} failed";

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
        public void SetStats(string externalId, long published, long sendErrors, long dropped, DateTimeOffset? lastPublishUtc)
        {
            var d = byId[externalId];
            if (d.Published == published && d.SendErrors == sendErrors && d.Dropped == dropped && d.LastPublishUtc == lastPublishUtc) return;
            d.Published = published;
            d.SendErrors = sendErrors;
            d.Dropped = dropped;
            d.LastPublishUtc = lastPublishUtc;
            Version++;
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
                default: state = d.Stage == DeviceStage.Connecting ? "connecting" : d.Stage == DeviceStage.Ready ? "ready" : "publishing"; break;
            }

            var text = $"{id} · {state}";
            if (d.Stage == DeviceStage.Publishing || d.Published > 0)
            {
                text += $" · {d.Published} sent";
                if (d.LastPublishUtc.HasValue) text += $" · last {d.LastPublishUtc.Value.UtcDateTime:HH:mm:ss}Z";
            }

            if (d.SendErrors > 0) text += $" · {d.SendErrors} send errors";
            if (d.Dropped > 0) text += $" · {d.Dropped} dropped";
            text += Command(d);
            var ok = d.Stage == DeviceStage.Publishing && d.Side == DeviceSide.None;
            return new PanelLine(text, ok ? LineKind.Ok : LineKind.Pending);
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

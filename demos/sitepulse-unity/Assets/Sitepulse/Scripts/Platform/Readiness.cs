// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>
    /// How far a device has got. This slice stops at Credentialed; sessions and observation extend
    /// the ladder later. A failure is a side state, not a stage.
    /// </summary>
    public enum DeviceStage { Unbound, Resolved, Credentialed }

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

        public bool Failed => FailReason != null;
    }

    /// <summary>
    /// The per-device readiness the panel shows. Counts are exact: <c>n/19 credentialed · k failed</c>
    /// counts devices at that state and never rounds up, and a device is one or the other, so
    /// pending devices are in neither number.
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

        public string Summary() => $"{CredentialedCount}/{Total} credentialed · {FailedCount} failed";

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
                if (d.Failed) lines.Add(new PanelLine(d.FailReason, LineKind.Failed));
                else if (d.Stage == DeviceStage.Credentialed)
                    lines.Add(new PanelLine($"{id} · credentialed · {d.Bind.DeviceToken} · {d.Path.ToString().ToLowerInvariant()}", LineKind.Ok));
                else if (d.Stage == DeviceStage.Resolved)
                    lines.Add(new PanelLine($"{id} · resolved · {d.Bind.DeviceToken} · credential pending", LineKind.Pending));
                else
                    lines.Add(new PanelLine($"{id} · resolving", LineKind.Pending));
            }

            return lines;
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

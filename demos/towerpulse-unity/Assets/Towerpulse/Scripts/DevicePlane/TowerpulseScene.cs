// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using System.Text.Json;
using DeviceChain.Demos.Platform;
using DeviceChain.Towerpulse.Plane;

namespace DeviceChain.Towerpulse.DevicePlane
{
    /// <summary>The kinds of device Towerpulse draws, as the binder's strings.</summary>
    public static class SceneKind
    {
        public const string Vav = "vav", Ahu = "ahu", Chiller = "chiller", Meter = "meter";

        public static string Of(PlaneKind k)
        {
            switch (k)
            {
                case PlaneKind.Vav: return Vav;
                case PlaneKind.Ahu: return Ahu;
                case PlaneKind.Chiller: return Chiller;
                default: return Meter;
            }
        }
    }

    /// <summary>What the binder checks the platform against: the device type each kind must resolve to, and the metrics and commands its profile must define.</summary>
    public static class TowerpulseScene
    {
        /// <summary>The name this demo registers with the shared platform package: log prefix and per-device credential names.</summary>
        public const string DemoName = "towerpulse";

        public static IReadOnlyList<SceneDevice> Devices(IReadOnlyList<PlaneDevice> devices)
        {
            var list = new List<SceneDevice>(devices.Count);
            foreach (var d in devices) list.Add(new SceneDevice(d.ExternalId, SceneKind.Of(d.Kind)));
            return list;
        }

        public static SceneContract Contract()
        {
            return new SceneContract
            {
                Zones = new HashSet<string>(),
                Kinds = new Dictionary<string, KindContract>
                {
                    { SceneKind.Vav, new KindContract { TypeToken = Wire.VavType, NumericMetrics = Wire.VavMetrics, Commands = new[] { Wire.BoostCooling, Wire.SetSetpoint } } },
                    { SceneKind.Ahu, new KindContract { TypeToken = Wire.AhuType, NumericMetrics = Wire.AhuMetrics, Commands = new[] { Wire.SetMode } } },
                    {
                        SceneKind.Chiller,
                        new KindContract
                        {
                            TypeToken = Wire.ChillerType, NumericMetrics = Wire.ChillerNumericMetrics, BooleanMetrics = Wire.ChillerBooleanMetrics,
                            Commands = new[] { Wire.Restart },
                        }
                    },
                    { SceneKind.Meter, new KindContract { TypeToken = Wire.MeterType, NumericMetrics = Wire.MeterMetrics } },
                },
            };
        }
    }

    /// <summary>Reads a command's payload into plain values, with <see cref="JsonElement"/> only (no serializer), so it is safe off the main thread and under IL2CPP.</summary>
    public static class PayloadReader
    {
        public static CommandArgs Read(JsonElement? payload)
        {
            var a = new CommandArgs();
            if (!payload.HasValue || payload.Value.ValueKind == JsonValueKind.Null || payload.Value.ValueKind == JsonValueKind.Undefined) return a;
            if (payload.Value.ValueKind != JsonValueKind.Object) { a.Malformed = true; return a; }
            foreach (var p in payload.Value.EnumerateObject())
            {
                switch (p.Value.ValueKind)
                {
                    case JsonValueKind.Number:
                        if (p.Value.TryGetDouble(out var d)) a.Numbers[p.Name] = d; else a.Other.Add(p.Name);
                        break;
                    case JsonValueKind.String: a.Strings[p.Name] = p.Value.GetString(); break;
                    default: a.Other.Add(p.Name); break;
                }
            }
            return a;
        }
    }
}

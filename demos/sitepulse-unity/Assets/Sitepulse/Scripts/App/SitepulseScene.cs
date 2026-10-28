// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Platform;
using UnityEngine;
using DeviceChain.Demos.Platform;

namespace DeviceChain.Sitepulse.App
{
    /// <summary>
    /// What the scene needs of the platform, assembled from the scene's own vocabulary: the machines
    /// in the choreography file, the measurement and command keys of <see cref="MeasurementKeys"/> /
    /// <see cref="CommandKeys"/>, and the zones of the feature file. Platform code stays free of
    /// scene knowledge; this is the one place the two meet.
    /// </summary>
    public static class SitepulseScene
    {
        public const string PlantId = "SP-PL-0001";

        // the device types the Sitepulse simulator provisions
        public const string HaulerType = "sp-hauler", LoaderType = "sp-loader", DozerType = "sp-dozer", PlantType = "sp-crusher-plant";

        [Serializable] sealed class FleetFile { public MachineEntry[] machines; }
        [Serializable] sealed class MachineEntry { public string id; public string kind; }
        [Serializable] sealed class FeatureFile { public ZoneEntry[] zones; }
        [Serializable] sealed class ZoneEntry { public string token; }

        /// <summary>The scene's devices: every machine of the choreography, then the plant.</summary>
        public static IReadOnlyList<SceneDevice> Devices(string choreographyJson, string plantId = PlantId)
        {
            var file = JsonUtility.FromJson<FleetFile>(choreographyJson);
            if (file == null || file.machines == null || file.machines.Length == 0)
                throw new FormatException("the choreography file lists no machines");
            var list = new List<SceneDevice>();
            foreach (var m in file.machines)
            {
                string kind;
                switch (m.kind)
                {
                    case "Hauler": kind = SceneKind.Hauler; break;
                    case "Loader": kind = SceneKind.Loader; break;
                    case "Dozer": kind = SceneKind.Dozer; break;
                    default: throw new FormatException($"machine {m.id} has kind \"{m.kind}\", which the binder does not know");
                }

                list.Add(new SceneDevice(m.id, kind));
            }

            list.Add(new SceneDevice(plantId, SceneKind.Plant));
            return list;
        }

        /// <summary>The zone tokens the scene has geometry for.</summary>
        public static ISet<string> Zones(string featuresJson)
        {
            var file = JsonUtility.FromJson<FeatureFile>(featuresJson);
            if (file == null || file.zones == null) throw new FormatException("the feature file has no zones");
            var zones = new HashSet<string>(StringComparer.Ordinal);
            foreach (var z in file.zones) zones.Add(z.token);
            return zones;
        }

        // a machine takes the equipment metrics and commands, and its goto-area lists areas; the plant takes its own
        // metrics and flags, and no command
        static KindContract Equipment(string typeToken) => new KindContract
        {
            TypeToken = typeToken,
            NumericMetrics = MeasurementKeys.Equipment,
            Commands = CommandKeys.Equipment,
            AreaCommand = CommandKeys.GotoArea,
        };

        public static SceneContract Contract(ISet<string> zones) => new SceneContract
        {
            Kinds = new Dictionary<string, KindContract>
            {
                [SceneKind.Hauler] = Equipment(HaulerType),
                [SceneKind.Loader] = Equipment(LoaderType),
                [SceneKind.Dozer] = Equipment(DozerType),
                [SceneKind.Plant] = new KindContract
                {
                    TypeToken = PlantType,
                    NumericMetrics = MeasurementKeys.Plant,
                    BooleanMetrics = MeasurementKeys.PlantFlags,
                },
            },
            Zones = zones,
        };
    }
}

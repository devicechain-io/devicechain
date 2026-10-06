// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sitepulse.Platform
{
    public enum SceneKind { Hauler, Loader, Dozer, Plant }

    /// <summary>One device the scene draws: the external ID it is authored under and what it is.</summary>
    public readonly struct SceneDevice
    {
        public SceneDevice(string externalId, SceneKind kind)
        {
            ExternalId = externalId ?? throw new ArgumentNullException(nameof(externalId));
            Kind = kind;
        }

        public string ExternalId { get; }
        public SceneKind Kind { get; }
    }

    /// <summary>
    /// What the scene needs of the platform, as data. Platform code holds no scene knowledge: the
    /// composition root builds this from the scene's own vocabulary (<c>MeasurementKeys</c>,
    /// <c>CommandKeys</c>, the zone catalogue), so a key renamed there is renamed here by the
    /// compiler and not by a second list that could drift.
    /// </summary>
    public sealed class SceneContract
    {
        /// <summary>The device type each kind must resolve to.</summary>
        public IReadOnlyDictionary<SceneKind, string> TypeTokens { get; set; }

        /// <summary>Numeric metric keys an equipment profile must define.</summary>
        public IReadOnlyList<string> EquipmentMetrics { get; set; }

        public IReadOnlyList<string> PlantMetrics { get; set; }

        /// <summary>Metric keys the plant profile must define as BOOLEAN.</summary>
        public IReadOnlyList<string> PlantFlags { get; set; }

        /// <summary>Command keys an equipment profile must define.</summary>
        public IReadOnlyList<string> EquipmentCommands { get; set; }

        /// <summary>The command whose parameter schema lists areas, and the zones the scene has geometry for.</summary>
        public string AreaCommand { get; set; }

        public ISet<string> Zones { get; set; }

        public bool IsPlant(SceneKind kind) => kind == SceneKind.Plant;
    }
}

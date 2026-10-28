// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Demos.Platform
{
    /// <summary>
    /// One device the scene draws: the external ID it is authored under and what it is. The kind is the
    /// demo's own vocabulary (a string it also uses as the key of <see cref="SceneContract.Kinds"/>);
    /// the platform code never interprets it.
    /// </summary>
    public readonly struct SceneDevice
    {
        public SceneDevice(string externalId, string kind)
        {
            ExternalId = externalId ?? throw new ArgumentNullException(nameof(externalId));
            Kind = kind ?? throw new ArgumentNullException(nameof(kind));
        }

        public string ExternalId { get; }
        public string Kind { get; }
    }

    /// <summary>What the scene needs of the platform for ONE kind of device, as data.</summary>
    public sealed class KindContract
    {
        /// <summary>The device type the kind must resolve to.</summary>
        public string TypeToken { get; set; }

        /// <summary>Metric keys the profile must define as numbers.</summary>
        public IReadOnlyList<string> NumericMetrics { get; set; } = Array.Empty<string>();

        /// <summary>Metric keys the profile must define as BOOLEAN.</summary>
        public IReadOnlyList<string> BooleanMetrics { get; set; } = Array.Empty<string>();

        /// <summary>Command keys the profile must define.</summary>
        public IReadOnlyList<string> Commands { get; set; } = Array.Empty<string>();

        /// <summary>
        /// The command whose parameter schema lists areas (checked against <see cref="SceneContract.Zones"/>), or null
        /// when the kind has none.
        /// </summary>
        public string AreaCommand { get; set; }
    }

    /// <summary>
    /// What the scene needs of the platform, as data. Platform code holds no scene knowledge: the
    /// composition root builds this from the scene's own vocabulary (its measurement keys, its command
    /// keys, its zone catalogue), so a key renamed there is renamed here by the compiler and not by a
    /// second list that could drift.
    /// </summary>
    public sealed class SceneContract
    {
        /// <summary>The contract of each kind of device, by the kind a <see cref="SceneDevice"/> carries.</summary>
        public IReadOnlyDictionary<string, KindContract> Kinds { get; set; }

        /// <summary>The areas the scene has geometry for.</summary>
        public ISet<string> Zones { get; set; }
    }
}

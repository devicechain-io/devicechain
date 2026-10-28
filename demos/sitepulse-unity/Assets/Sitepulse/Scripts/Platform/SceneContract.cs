// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>
    /// The kinds of device the quarry scene draws, spelled as the keys of the shared platform code's
    /// <c>SceneContract.Kinds</c>. A device of any other kind is a defect in the scene, not a new case.
    /// </summary>
    public static class SceneKind
    {
        public const string Hauler = "Hauler";
        public const string Loader = "Loader";
        public const string Dozer = "Dozer";
        public const string Plant = "Plant";

        public static readonly IReadOnlyList<string> All = new[] { Hauler, Loader, Dozer, Plant };
    }
}

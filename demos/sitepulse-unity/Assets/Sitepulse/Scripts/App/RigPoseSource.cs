// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Runtime.CompilerServices;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Visuals;
using UnityEngine;

[assembly: InternalsVisibleTo("DeviceChain.Sitepulse.Tests.EditMode")]

namespace DeviceChain.Sitepulse.App
{
    /// <summary>
    /// Reads the scene for the device plane, on the main thread: a machine's place from its rig's
    /// transform (Unity X east, Z north, y the terrain height it is grounded on), the way it faces from
    /// its forward vector, and whether it carries a load. A hauler carries when its rig says so; a
    /// loader carries when its bucket is racked back past the carry angle. The plant does not move and
    /// reports no position, so it answers with a fixed pose.
    /// </summary>
    public sealed class RigPoseSource : IPoseSource
    {
        // the loader's bucket is racked back (negative) when it holds a load; the carry angle is -38
        const float BucketCarryBelow = -33f;

        readonly Func<int> count;
        readonly Func<IEnumerable<MachineRig>> machines;
        readonly IotOverlay overlay;
        readonly Dictionary<string, MachineRig> rigs = new Dictionary<string, MachineRig>(StringComparer.Ordinal);
        int seen = -1;

        public RigPoseSource(QuarryFleetPreview fleet, IotOverlay overlay)
            : this(fleet != null ? (Func<int>)(() => fleet.Count) : null, fleet != null ? (Func<IEnumerable<MachineRig>>)(() => fleet.Machines) : null, overlay)
        {
        }

        internal RigPoseSource(Func<int> count, Func<IEnumerable<MachineRig>> machines, IotOverlay overlay)
        {
            this.count = count;
            this.machines = machines;
            this.overlay = overlay;
        }

        // the cache is keyed to the fleet's size, and also dropped when a cached rig has been destroyed
        // (the fleet respawned, even at the same size), so a destroyed rig is never read again
        void Refresh(bool force)
        {
            if (count == null || machines == null) return;
            if (!force && count() == seen) return;
            rigs.Clear();
            foreach (var rig in machines()) rigs[rig.name] = rig;
            seen = count();
        }

        public bool TryGet(string externalId, out MachinePose pose)
        {
            if (overlay != null && externalId == overlay.plantId)
            {
                var p = overlay.plant != null ? overlay.plant.position : Vector3.zero;
                pose = new MachinePose(p.x, p.z, p.y, 0.0, false);
                return true;
            }

            Refresh(false);
            if (rigs.TryGetValue(externalId, out var r) && r == null)
            {
                Refresh(true);
                rigs.TryGetValue(externalId, out r);
            }

            if (r == null)
            {
                pose = default;
                return false;
            }

            var t = r.transform;
            var f = t.forward;
            var facing = Mathf.Atan2(f.x, f.z) * Mathf.Rad2Deg;
            var loaded = r.Kind == MachineKind.Hauler ? r.loaded : r.Kind == MachineKind.Loader && r.bucket < BucketCarryBelow;
            pose = new MachinePose(t.position.x, t.position.z, t.position.y, facing, loaded);
            return true;
        }
    }
}

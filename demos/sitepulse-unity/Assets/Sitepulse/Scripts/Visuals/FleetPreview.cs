// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using UnityEngine;

namespace DeviceChain.Sitepulse.Visuals
{
    /// <summary>
    /// A self-contained look at the fleet models: spawns each machine with its LOD group, drives
    /// it back and forth along a lane and cycles its work implement. It exists to check the
    /// models and rigs by eye, and has no part in the site simulation.
    /// </summary>
    public sealed class FleetPreview : MonoBehaviour
    {
        [System.Serializable]
        public struct Model
        {
            public MachineKind kind;
            public GameObject lod0;
            public GameObject lod1;
        }

        public List<Model> models = new List<Model>();
        [Min(1)] public int perKind = 1;
        public float laneLength = 30f;
        public float laneSpacing = 9f;
        public float speed = 3f;
        public float cycleSeconds = 12f;

        sealed class Unit
        {
            public MachineRig rig;
            public Vector3 start;
            public float s, phase;
        }

        readonly List<Unit> units = new List<Unit>();

        void Start()
        {
            int lane = 0;
            foreach (var m in models)
            {
                if (m.lod0 == null || m.lod1 == null) continue;
                for (int i = 0; i < perKind; i++, lane++)
                {
                    var go = FleetRig.BuildMergedLod(m.lod0, m.lod1, $"{m.kind}_{i:00}", out var error);
                    if (go == null)
                    {
                        Debug.LogError($"[fleet-preview] {m.kind}: {error}");
                        continue;
                    }
                    go.transform.SetParent(transform, false);
                    var start = new Vector3(lane * laneSpacing, 0f, 0f);
                    go.transform.localPosition = start;
                    var rig = go.AddComponent<MachineRig>();
                    rig.Bind(m.kind);
                    units.Add(new Unit { rig = rig, start = start, phase = lane * 0.37f });
                }
            }
        }

        void Update()
        {
            float dt = Time.deltaTime;
            foreach (var u in units)
            {
                // Out along +Z and back in reverse: one cycle is a full lane each way.
                float t = Mathf.Repeat(Time.time / cycleSeconds + u.phase, 1f);
                float s = Mathf.PingPong(t * 2f, 1f) * laneLength;
                float step = s - u.s;
                u.s = s;
                u.rig.transform.localPosition = u.start + Vector3.forward * s;
                u.rig.AddTravel(step);
                float w = 0.5f - 0.5f * Mathf.Cos(t * 4f * Mathf.PI);   // 0..1, twice per cycle
                switch (u.rig.Kind)
                {
                    case MachineKind.Dozer:
                        u.rig.bladeArm = Mathf.Lerp(FleetRig.DozerBladeRaised, FleetRig.DozerBladeDig * 0.5f, w);
                        u.rig.ripper = Mathf.Lerp(0f, FleetRig.DozerRipperLowered * 0.5f, 1f - w);
                        break;
                    case MachineKind.Loader:
                        u.rig.boom = Mathf.Lerp(FleetRig.LoaderBoomCarry, FleetRig.LoaderBoomMaxLift, w);
                        u.rig.bucket = Mathf.Lerp(FleetRig.LoaderBucketCarry, 60f, w);
                        u.rig.steer = Mathf.Sin(t * 2f * Mathf.PI) * 20f;
                        break;
                    case MachineKind.Hauler:
                        u.rig.dump = Mathf.Lerp(0f, FleetRig.HaulerDumpMax, Mathf.Clamp01(w * 1.4f - 0.2f));
                        u.rig.steer = Mathf.Sin(t * 2f * Mathf.PI) * 15f;
                        break;
                }
            }
        }
    }
}

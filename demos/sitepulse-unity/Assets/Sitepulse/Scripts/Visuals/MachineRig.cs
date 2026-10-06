// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using UnityEngine;

namespace DeviceChain.Sitepulse.Visuals
{
    /// <summary>
    /// Drives the moving parts of one machine model from a small set of inputs: the work
    /// implement angles, steering, and distance travelled. Whatever moves the machine (a route
    /// controller, a preview script) sets these; this component only turns them into node
    /// rotations, once per frame in <c>LateUpdate</c>, after which it re-aims the hydraulics.
    ///
    /// It never moves the root transform. Wheel spin and tread scroll come from
    /// <see cref="AddTravel"/> / <see cref="AddTrackTravel"/>, so reversing is just a negative
    /// distance.
    /// </summary>
    [DisallowMultipleComponent]
    public sealed class MachineRig : MonoBehaviour
    {
        [SerializeField] MachineKind kind;

        [Header("Dozer")]
        [Tooltip("BladeArmPivot X (deg): negative raises, positive digs.")]
        [Range(FleetRig.DozerBladeRaised, FleetRig.DozerBladeDig)] public float bladeArm;
        [Tooltip("Ripper X (deg): 0 stowed, negative lowers.")]
        [Range(FleetRig.DozerRipperLowered, 0f)] public float ripper;

        [Header("Loader")]
        [Tooltip("Boom X (deg): 0 on the ground, negative raises.")]
        [Range(FleetRig.LoaderBoomMaxLift, 0f)] public float boom;
        [Tooltip("Bucket X relative to the boom (deg): negative racks back, positive dumps.")]
        [Range(FleetRig.LoaderBucketRackBack, FleetRig.LoaderBucketDump)] public float bucket;

        [Header("Hauler")]
        [Tooltip("Dump body raise (deg), 0 at rest.")]
        [Range(0f, FleetRig.HaulerDumpMax)] public float dump;
        [Tooltip("Whether the hauler is carrying a load. The heap shows only when loaded and the body is down.")]
        public bool loaded = true;

        [Header("Steering")]
        [Tooltip("Loader articulation or hauler front-wheel steer (deg); positive turns right.")]
        public float steer;

        public MachineKind Kind => kind;

        readonly List<Transform> wheels = new List<Transform>();
        readonly List<(Transform part, Transform target)> aims = new List<(Transform, Transform)>();
        readonly List<(Renderer renderer, int material, Vector4 st, bool left)> treads = new List<(Renderer, int, Vector4, bool)>();
        Transform a, b, c, d;
        Transform load;
        float wheelAngle, leftTrack, rightTrack;
        bool loadVisible = true;
        MaterialPropertyBlock block;
        bool bound;

        static readonly int TreadST = Shader.PropertyToID("baseColorTexture_ST");

        /// <summary>Bind to a model already parented under this GameObject (or this GameObject itself).</summary>
        public void Bind(MachineKind machineKind)
        {
            kind = machineKind;
            Rebind();
        }

        void Awake()
        {
            if (!bound) Rebind();
        }

        void Rebind()
        {
            wheels.Clear();
            treads.Clear();
            aims.Clear();
            var root = transform;
            switch (kind)
            {
                case MachineKind.Dozer:
                    a = FleetRig.Find(root, "BladeArmPivot");
                    b = FleetRig.Find(root, "Blade");
                    c = FleetRig.Find(root, "Ripper");
                    d = FleetRig.Find(root, "RipperCarriage");
                    foreach (var n in new[] { "LeftSprocket", "LeftIdler", "RightSprocket", "RightIdler" })
                        Add(wheels, FleetRig.Find(root, n));
                    BindTreads(root);
                    break;
                case MachineKind.Loader:
                    a = FleetRig.Find(root, "Boom");
                    b = FleetRig.Find(root, "Bucket");
                    c = FleetRig.Find(root, "Bellcrank");
                    d = FleetRig.Find(root, "FrontFrame");
                    AddWheels(root);
                    break;
                case MachineKind.Hauler:
                    a = FleetRig.Find(root, "DumpBody");
                    b = FleetRig.Find(root, "SteerFrontLeft");
                    c = FleetRig.Find(root, "SteerFrontRight");
                    load = FleetRig.Find(root, "Load");
                    AddWheels(root);
                    break;
            }
            aims.AddRange(FleetRig.AimPairs(root));
            block ??= new MaterialPropertyBlock();
            bound = true;
        }

        static void Add(List<Transform> list, Transform t)
        {
            if (t != null) list.Add(t);
        }

        void AddWheels(Transform root)
        {
            foreach (var n in new[] { "WheelFrontLeft", "WheelFrontRight", "WheelRearLeft", "WheelRearRight" })
                Add(wheels, FleetRig.Find(root, n));
        }

        // The track belts carry the tread texture on their M_Track material slot, at every LOD.
        void BindTreads(Transform root)
        {
            foreach (var r in root.GetComponentsInChildren<Renderer>(true))
            {
                bool left = r.name.StartsWith("LeftTrack_", System.StringComparison.Ordinal);
                if (!left && !r.name.StartsWith("RightTrack_", System.StringComparison.Ordinal)) continue;
                var mats = r.sharedMaterials;
                for (int i = 0; i < mats.Length; i++)
                {
                    if (mats[i] == null || !mats[i].name.StartsWith("M_Track", System.StringComparison.Ordinal)) continue;
                    var st = mats[i].HasProperty(TreadST) ? mats[i].GetVector(TreadST) : new Vector4(1, 1, 0, 0);
                    treads.Add((r, i, st, left));
                }
            }
        }

        /// <summary>Wheeled machines: roll every wheel by <paramref name="metres"/> (negative reverses).</summary>
        public void AddTravel(float metres)
        {
            if (kind == MachineKind.Dozer)
            {
                AddTrackTravel(metres, metres);
                return;
            }
            float radius = kind == MachineKind.Loader ? FleetRig.LoaderWheelRadius : FleetRig.HaulerWheelRadius;
            wheelAngle = Mathf.Repeat(wheelAngle + metres / radius * Mathf.Rad2Deg, 360f);
        }

        /// <summary>Dozer: advance each track by its own distance, so a pivot turn counter-rotates them.</summary>
        public void AddTrackTravel(float leftMetres, float rightMetres)
        {
            leftTrack += leftMetres;
            rightTrack += rightMetres;
        }

        /// <summary>Whether the hauler's load heap is currently shown.</summary>
        public bool LoadVisible => loadVisible;

        void LateUpdate() => Apply();

        /// <summary>Write the current inputs to the model's nodes. Called every frame; public for edit-time posing.</summary>
        public void Apply()
        {
            if (!bound) Rebind();
            switch (kind)
            {
                case MachineKind.Dozer: ApplyDozer(); break;
                case MachineKind.Loader: ApplyLoader(); break;
                case MachineKind.Hauler: ApplyHauler(); break;
            }
            FleetRig.AimAll(aims);
        }

        static Quaternion RX(float deg) => Quaternion.Euler(deg, 0f, 0f);
        static Quaternion RY(float deg) => Quaternion.Euler(0f, deg, 0f);

        void ApplyDozer()
        {
            float arm = Mathf.Clamp(bladeArm, FleetRig.DozerBladeRaised, FleetRig.DozerBladeDig);
            float rip = Mathf.Clamp(ripper, FleetRig.DozerRipperLowered, 0f);
            if (a) a.localRotation = RX(arm);
            if (b) b.localRotation = RX(-FleetRig.BladePitchComp * arm);
            if (c) c.localRotation = RX(rip);
            if (d) d.localRotation = RX(-rip);
            foreach (var w in wheels)
            {
                float dist = w.name.StartsWith("Left", System.StringComparison.Ordinal) ? leftTrack : rightTrack;
                w.localRotation = RX(Mathf.Repeat(dist / FleetRig.DozerSprocketRadius * Mathf.Rad2Deg, 360f));
            }
            foreach (var (r, mat, st, left) in treads)
            {
                float u = Mathf.Repeat(-(left ? leftTrack : rightTrack) * FleetRig.DozerTreadUPerMetre, 1f);
                r.GetPropertyBlock(block, mat);
                block.SetVector(TreadST, new Vector4(st.x, st.y, st.z + u, st.w));
                r.SetPropertyBlock(block, mat);
            }
        }

        void ApplyLoader()
        {
            float bk = Mathf.Clamp(bucket, FleetRig.LoaderBucketRackBack, FleetRig.LoaderBucketDump);
            if (a) a.localRotation = RX(Mathf.Clamp(boom, FleetRig.LoaderBoomMaxLift, 0f));
            if (b) b.localRotation = RX(bk);
            if (c) c.localRotation = RX(FleetRig.LoaderBellcrankDeg(bk));
            if (d) d.localRotation = RY(Mathf.Clamp(steer, -FleetRig.LoaderArticulationLimit, FleetRig.LoaderArticulationLimit));
            foreach (var w in wheels) w.localRotation = RX(wheelAngle);
        }

        void ApplyHauler()
        {
            float bodyX = -Mathf.Clamp(dump, 0f, FleetRig.HaulerDumpMax);
            var st = RY(Mathf.Clamp(steer, -FleetRig.HaulerSteerLimit, FleetRig.HaulerSteerLimit));
            if (a) a.localRotation = RX(bodyX);
            if (b) b.localRotation = st;
            if (c) c.localRotation = st;
            foreach (var w in wheels) w.localRotation = RX(wheelAngle);
            loadVisible = FleetRig.HaulerLoadVisible(bodyX, loadVisible);
            if (load && load.gameObject.activeSelf != (loadVisible && loaded)) load.gameObject.SetActive(loadVisible && loaded);
        }
    }
}

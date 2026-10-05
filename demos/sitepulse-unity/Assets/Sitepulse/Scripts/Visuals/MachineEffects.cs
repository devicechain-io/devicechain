// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using UnityEngine;

namespace DeviceChain.Sitepulse.Visuals
{
    /// <summary>
    /// The cosmetic effects of one machine, driven from what its rig is doing: dust behind it
    /// that grows with its speed, exhaust that puffs when it pulls away or climbs, a rotating
    /// amber beacon while it works, and falling rock when a hauler tips its body or a loader its
    /// bucket. A machine that has not moved for a while is taken to be shut down: no exhaust, no
    /// beacon. Local presentation only.
    /// </summary>
    [DisallowMultipleComponent]
    public sealed class MachineEffects : MonoBehaviour, IQuarryEffect
    {
        MachineRig rig;
        QuarryEffects fx;
        float budget = 1f;

        ParticleSystem dust, exhaust, pour, pourDust, bladeDust;
        Transform exhaustTip, beaconNode, dumpBody, bucket, pourFloor;
        MeshRenderer flare;
        MaterialPropertyBlock flareBlock, beaconBlock;
        Renderer[] beaconRenderers = System.Array.Empty<Renderer>();
        Color beaconBase = new Color(1f, 0.42f, 0.02f);

        Vector3 lastPos;
        bool hasLast;
        float speed, accel, climb, clock, lastMove = -1e6f, phase;
        float lastDump, lastBucket, bucketTipped;
        bool tipping;

        static readonly int FlareColor = Shader.PropertyToID("_BaseColor");
        static readonly int Emissive = Shader.PropertyToID("emissiveFactor");

        public void Bind(MachineRig machine, QuarryEffects effects)
        {
            rig = machine;
            fx = effects;
            if (fx == null) return;
            budget = fx.Budget;
            phase = Mathf.Abs(name.GetHashCode() % 1000) * 0.013f;
            var root = transform;
            exhaustTip = FleetRig.Find(root, "ExhaustTip");
            beaconNode = FleetRig.Find(root, "Beacon");
            var dustColor = new Color(0.86f, 0.80f, 0.70f, 1f);
            switch (rig.Kind)
            {
                case MachineKind.Hauler:
                    dust = QuarryEffects.Emitter(root, "Dust", new Vector3(0f, 0.5f, -3.4f), fx.dust, Max(90));
                    QuarryEffects.AsDust(dust, dustColor, new Vector2(2.8f, 5.2f), new Vector2(3f, 5f), 0.6f);
                    QuarryEffects.Box(dust, new Vector3(4.6f, 0.4f, 1.2f));
                    dumpBody = FleetRig.Find(root, "DumpBody");
                    if (dumpBody != null)
                    {
                        var b = LocalBounds(dumpBody, "Load");
                        // the body's open tail: material spills out across its whole height as the body
                        // tips, a curtain from the lip down to the ground behind the truck
                        pour = QuarryEffects.Emitter(dumpBody, "Pour", new Vector3(b.center.x, b.min.y + 1.2f, b.min.z + 0.3f), fx.rock, Max(300), fx.chunk);
                        QuarryEffects.AsChunks(pour, new Vector2(0.14f, 0.36f), 1.0f, new Vector2(1.2f, 3.0f));
                        QuarryEffects.Box(pour, new Vector3(b.size.x * 0.8f, 1.9f, 0.4f), new Vector3(0f, 180f, 0f));
                        pourDust = QuarryEffects.Emitter(root, "PourDust", new Vector3(0f, 0.8f, -7.5f), fx.dust, Max(50));
                        QuarryEffects.AsDust(pourDust, dustColor, new Vector2(3.5f, 7f), new Vector2(4f, 7f), 0.75f, 0.08f);
                        QuarryEffects.Box(pourDust, new Vector3(5f, 1f, 3f));
                    }
                    break;
                case MachineKind.Loader:
                    dust = QuarryEffects.Emitter(root, "Dust", new Vector3(0f, 0.4f, -2.2f), fx.dust, Max(50));
                    QuarryEffects.AsDust(dust, dustColor, new Vector2(1.6f, 3.2f), new Vector2(2.5f, 4f), 0.45f);
                    QuarryEffects.Box(dust, new Vector3(2.8f, 0.3f, 1f));
                    bucket = FleetRig.Find(root, "Bucket");
                    if (bucket != null)
                    {
                        var b = LocalBounds(bucket, null);
                        pour = QuarryEffects.Emitter(bucket, "Pour", new Vector3(b.center.x, b.min.y + 0.3f, b.max.z - 0.2f), fx.rock, Max(140), fx.chunk);
                        QuarryEffects.AsChunks(pour, new Vector2(0.18f, 0.42f), 1.1f, new Vector2(0.3f, 1.2f));
                        QuarryEffects.Box(pour, new Vector3(b.size.x * 0.8f, 0.2f, 0.2f));
                        // the rock lands in what the bucket is over (a truck body, a hopper) and
                        // goes no further: a plane under the lip that every chunk dies on, so none
                        // falls through a wall that is not there to stop it
                        pourFloor = new GameObject("PourFloor") { hideFlags = HideFlags.DontSave }.transform;
                        pourFloor.SetParent(transform, false);
                        var col = pour.collision;
                        col.enabled = true;
                        col.type = ParticleSystemCollisionType.Planes;
                        col.SetPlane(0, pourFloor);
                        col.lifetimeLoss = 1f;
                        col.bounce = 0f;
                        col.dampen = 1f;
                        col.radiusScale = 0.3f;
                        pourDust = QuarryEffects.Emitter(bucket, "PourDust", new Vector3(b.center.x, b.min.y - 0.5f, b.max.z), fx.dust, Max(40));
                        QuarryEffects.AsDust(pourDust, dustColor, new Vector2(2.4f, 4.2f), new Vector2(2.5f, 4.5f), 0.6f, 0.08f);
                        QuarryEffects.Box(pourDust, new Vector3(2.5f, 0.6f, 0.8f));
                    }
                    break;
                case MachineKind.Dozer:
                    dust = QuarryEffects.Emitter(root, "Dust", new Vector3(0f, 0.3f, -0.5f), fx.dust, Max(40));
                    QuarryEffects.AsDust(dust, dustColor, new Vector2(1.6f, 3f), new Vector2(2.5f, 4f), 0.4f);
                    QuarryEffects.Box(dust, new Vector3(3.4f, 0.3f, 4f));
                    bladeDust = QuarryEffects.Emitter(root, "BladeDust", new Vector3(0f, 0.5f, 4.2f), fx.dust, Max(40));
                    QuarryEffects.AsDust(bladeDust, dustColor, new Vector2(1.6f, 3f), new Vector2(2.5f, 4f), 0.5f);
                    QuarryEffects.Box(bladeDust, new Vector3(4f, 0.4f, 0.6f));
                    break;
            }
            if (exhaustTip != null)
            {
                exhaust = QuarryEffects.Emitter(exhaustTip, "Exhaust", Vector3.zero, fx.exhaust, Max(30));
                QuarryEffects.AsDust(exhaust, new Color(0.28f, 0.28f, 0.29f, 1f), new Vector2(0.35f, 0.6f), new Vector2(1.2f, 1.8f), 0.45f, 0.06f);
                QuarryEffects.Cone(exhaust, 8f, 0.05f, Vector3.zero);
                var main = exhaust.main;
                main.startSpeed = new ParticleSystem.MinMaxCurve(1.6f, 2.6f);
            }
            if (beaconNode != null && fx.flare != null)
            {
                beaconRenderers = beaconNode.GetComponentsInChildren<Renderer>(true);
                foreach (var r in beaconRenderers)
                    if (r.sharedMaterial != null && r.sharedMaterial.HasProperty(Emissive))
                    {
                        beaconBase = r.sharedMaterial.GetColor(Emissive);
                        break;
                    }
                flare = QuarryEffects.Flare(beaconNode, "BeaconFlare", fx.flare);
                flare.transform.localPosition = new Vector3(0f, 0.12f, 0f);
                flareBlock = new MaterialPropertyBlock();
                beaconBlock = new MaterialPropertyBlock();
            }
            QuarryEffects.Active.Add(this);
        }

        int Max(int n) => Mathf.Max(4, Mathf.RoundToInt(n * budget));

        /// <summary>How far below a loader's bucket lip its falling rock lands (m).</summary>
        public const float PourDepth = 1.1f;

        void OnDestroy() => QuarryEffects.Active.Remove(this);

        /// <summary>Bounds of the meshes under <paramref name="node"/>, in its own space.</summary>
        static Bounds LocalBounds(Transform node, string skip)
        {
            bool any = false;
            var b = new Bounds();
            foreach (var mf in node.GetComponentsInChildren<MeshFilter>(true))
            {
                if (mf.sharedMesh == null) continue;
                if (skip != null && FleetRig.Find(node, skip) is Transform s && mf.transform.IsChildOf(s)) continue;
                var mb = mf.sharedMesh.bounds;
                for (int i = 0; i < 8; i++)
                {
                    var c = mb.center + Vector3.Scale(mb.extents, new Vector3((i & 1) == 0 ? -1 : 1, (i & 2) == 0 ? -1 : 1, (i & 4) == 0 ? -1 : 1));
                    var p = node.InverseTransformPoint(mf.transform.TransformPoint(c));
                    if (!any) { b = new Bounds(p, Vector3.zero); any = true; }
                    else b.Encapsulate(p);
                }
            }
            return b;
        }

        public void ClearParticles()
        {
            foreach (var ps in new[] { dust, exhaust, pour, pourDust, bladeDust })
                if (ps != null) ps.Clear(true);
            hasLast = false;
            // start the clock over too, so a still stepped up to a moment shows the same beacon
            // phase and the same working state whatever was stepped before it
            clock = 0f;
            lastMove = -1e6f;
            bucketTipped = 0f;
            speed = accel = climb = 0f;
        }

        public void Step(float dt, bool simulate)
        {
            if (rig == null || dt <= 0f) return;
            clock += dt;
            var pos = transform.position;
            if (hasLast)
            {
                var d = pos - lastPos;
                float inst = new Vector2(d.x, d.z).magnitude / dt;
                if (inst < 30f)
                {
                    float k = 1f - Mathf.Exp(-dt * 5f);
                    float before = speed;
                    speed = Mathf.Lerp(speed, inst, k);
                    accel = Mathf.Lerp(accel, (speed - before) / dt, k);
                    climb = Mathf.Lerp(climb, d.y / dt, k);
                    if (inst > 0.05f) lastMove = clock;
                }
            }
            lastPos = pos;
            hasLast = true;
            bool working = clock - lastMove < 12f;

            switch (rig.Kind)
            {
                case MachineKind.Hauler:
                    // dust with speed, and more where the wheels work hardest: climbing with a load
                    float haulDust = Mathf.Clamp01((speed - 0.5f) / 6.5f) + (rig.loaded ? 0.5f : 0.25f) * Mathf.Clamp01(climb / 0.25f) * Mathf.Clamp01(speed / 2f);
                    QuarryEffects.Rate(dust, budget * 22f * haulDust);
                    // tipping: from the moment the body lifts until it starts back down
                    if (rig.dump > 4f && lastDump <= 4f) tipping = true;
                    if (rig.dump < lastDump - 0.01f) tipping = false;
                    float flow = tipping ? Mathf.SmoothStep(0f, 1f, Mathf.InverseLerp(14f, 32f, rig.dump)) : 0f;
                    QuarryEffects.Rate(pour, budget * 260f * flow);
                    QuarryEffects.Rate(pourDust, budget * 14f * flow);
                    lastDump = rig.dump;
                    break;
                case MachineKind.Loader:
                    QuarryEffects.Rate(dust, budget * 9f * Mathf.Clamp01((speed - 0.5f) / 2.5f));
                    // tipping the bucket with the boom up: rock out over the lip, for as long as the
                    // bucket is held tipped (it may settle back a little) until it is empty
                    bool dumping = rig.boom < -35f && rig.bucket > 25f && rig.bucket >= lastBucket - dt * 20f;
                    bucketTipped = dumping ? bucketTipped + dt : 0f;
                    float bflow = dumping ? Mathf.InverseLerp(25f, 60f, rig.bucket) * (1f - Mathf.SmoothStep(0f, 1f, Mathf.InverseLerp(2f, 3.2f, bucketTipped))) : 0f;
                    QuarryEffects.Rate(pour, budget * 170f * bflow);
                    QuarryEffects.Rate(pourDust, budget * 20f * bflow);
                    if (pour != null)
                    {
                        pour.transform.rotation = Quaternion.LookRotation(Vector3.down, transform.forward);
                        // 1.1 m below the lip: inside a truck body or the hopper, under its rim
                        pourFloor.SetPositionAndRotation(pour.transform.position + Vector3.down * PourDepth, Quaternion.identity);
                    }
                    lastBucket = rig.bucket;
                    break;
                case MachineKind.Dozer:
                    QuarryEffects.Rate(dust, budget * 6f * Mathf.Clamp01(speed / 1.5f));
                    QuarryEffects.Rate(bladeDust, rig.bladeArm > 1f ? budget * 12f * Mathf.Clamp01(speed / 1.2f) : 0f);
                    break;
            }

            if (exhaust != null)
            {
                exhaust.transform.rotation = Quaternion.LookRotation(Vector3.up, transform.forward);
                float throttle = Mathf.Clamp01(accel / 0.5f) + Mathf.Clamp01(climb / 0.8f) * 0.7f;
                QuarryEffects.Rate(exhaust, working ? budget * (1.5f + 16f * Mathf.Clamp01(throttle)) : 0f);
            }

            if (flare != null)
            {
                flare.enabled = working;
                // a rotating beacon seen from one side: a lens that always glows amber, and two
                // sharp flashes per turn on top of it
                float s = Mathf.Sin((clock * 1.3f + phase) * Mathf.PI * 2f);
                float pulse = Mathf.Pow(Mathf.Abs(s), 6f);
                var cam = Camera.main;
                if (working && cam != null)
                {
                    var t = flare.transform;
                    t.rotation = Quaternion.LookRotation(t.position - cam.transform.position, cam.transform.up);
                    float dist = Vector3.Distance(t.position, cam.transform.position);
                    float size = (0.6f + 1.0f * pulse) * Mathf.Clamp(dist / 40f, 1f, 3f);
                    t.localScale = new Vector3(size, size, size);
                    // amber kept below the tone mapper's shoulder, which would turn a hotter one yellow
                    flareBlock.SetColor(FlareColor, new Color(1.6f, 0.5f, 0.06f, 0.5f + 0.5f * pulse));
                    flare.SetPropertyBlock(flareBlock);
                }
                foreach (var r in beaconRenderers)
                {
                    if (r == null || r == flare) continue;
                    r.GetPropertyBlock(beaconBlock);
                    beaconBlock.SetColor(Emissive, beaconBase * (working ? 1.1f + 2.0f * pulse : 0.15f));
                    r.SetPropertyBlock(beaconBlock);
                }
            }

            QuarryEffects.Advance(dust, dt, simulate);
            QuarryEffects.Advance(exhaust, dt, simulate);
            QuarryEffects.Advance(pour, dt, simulate);
            QuarryEffects.Advance(pourDust, dt, simulate);
            QuarryEffects.Advance(bladeDust, dt, simulate);
        }
    }
}

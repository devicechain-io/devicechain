// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using UnityEngine;

namespace DeviceChain.Sitepulse.Visuals
{
    /// <summary>
    /// Brings the processing plant to life: its belts carry rock (the texture of the rock layer on
    /// each belt scrolls at the belt's speed), a stream of rock falls from each conveyor's head
    /// onto its stockpile, with dust where it lands, and a thin dust haze hangs over the crusher
    /// and the screen; the crusher's flywheels turn and the screen box shakes. It finds the
    /// plant's nodes (StackerHead, SideHeadRight, SideHeadLeft, Hopper, Screen, CrusherDischarge,
    /// FlywheelLeft, FlywheelRight, ScreenBox) by name. Local presentation only.
    /// </summary>
    [ExecuteAlways]
    public sealed class PlantEffects : MonoBehaviour, IQuarryEffect
    {
        [Tooltip("The processing plant (the crusher_plant prefab instance).")]
        public Transform plant;
        public QuarryTerrain terrain;
        public QuarryEffects effects;
        [Tooltip("Belt speed (m/s).")]
        public float beltSpeed = 2.5f;
        [Tooltip("Metres of belt per repeat of the rock texture (ArtSource: 2 m, times the plant's scale).")]
        public float beltPeriod = 2.8f;
        [Tooltip("Flywheel speed (rad/s).")]
        public float flywheelSpeed = 4f;
        [Tooltip("Screen box shake: amplitude (m) and frequency (Hz), exaggerated so that it reads.")]
        public Vector2 screenShake = new Vector2(0.015f, 8f);

        /// <summary>The nodes of the plant that move, which must not be marked static.</summary>
        public static readonly string[] MovingNodes = { "FlywheelLeft", "FlywheelRight", "ScreenBox" };

        readonly List<ParticleSystem> systems = new List<ParticleSystem>();
        readonly List<(Renderer r, int slot)> belts = new List<(Renderer, int)>();
        MaterialPropertyBlock block;
        GameObject holder;
        Transform flywheelLeft, flywheelRight, screenBox;
        Vector3 screenRest;
        float clock;

        static readonly int BaseST = Shader.PropertyToID("baseColorTexture_ST");

        void OnEnable() => QuarryEffects.Active.Add(this);

        void OnDisable()
        {
            QuarryEffects.Active.Remove(this);
            Clear();
        }

        void Clear()
        {
            systems.Clear();
            belts.Clear();
            if (holder != null) DestroyImmediate(holder);
            holder = null;
        }

        void Build()
        {
            Clear();
            if (plant == null || effects == null) return;
            float budget = effects.Budget;
            int Max(int n) => Mathf.Max(4, Mathf.RoundToInt(n * budget));
            holder = new GameObject("Plant Effects (generated)") { hideFlags = HideFlags.DontSave };
            holder.transform.SetParent(transform, false);
            var stone = new Color(0.82f, 0.83f, 0.84f, 1f);
            var haze = new Color(0.80f, 0.77f, 0.72f, 1f);
            foreach (var (node, dir) in new[] { ("StackerHead", Vector3.forward), ("SideHeadRight", Vector3.right), ("SideHeadLeft", Vector3.left) })
            {
                var head = FleetRig.Find(plant, node);
                if (head == null) continue;
                // a falling stream: chunks thrown off the head pulley, landing on the pile below
                var p = head.position;
                float pile = terrain != null && terrain.Built ? terrain.HeightAt(p.x, p.z) : p.y - 4f;
                float drop = Mathf.Max(0.5f, p.y - pile - 0.2f);
                float fall = Mathf.Sqrt(2f * drop / 9.81f);
                var stream = QuarryEffects.Emitter(head, "Stream", Vector3.zero, effects.rock, Max(220), effects.chunk);
                QuarryEffects.AsChunks(stream, new Vector2(0.1f, 0.24f), fall + 0.05f, new Vector2(0.9f, 1.4f));
                QuarryEffects.Box(stream, new Vector3(0.7f, 0.1f, 0.3f));
                stream.transform.rotation = Quaternion.LookRotation(plant.TransformDirection(dir), Vector3.up);
                QuarryEffects.Rate(stream, budget * 140f);
                systems.Add(stream);
                var land = QuarryEffects.Emitter(holder.transform, node + "Dust", Vector3.zero, effects.dust, Max(24));
                land.transform.position = new Vector3(p.x, pile + 0.5f, p.z) + plant.TransformDirection(dir) * 0.8f;
                QuarryEffects.AsDust(land, stone, new Vector2(2f, 4f), new Vector2(4f, 6f), 0.35f, 0.05f);
                QuarryEffects.Box(land, new Vector3(2f, 0.5f, 2f));
                QuarryEffects.Rate(land, budget * 3.5f);
                systems.Add(land);
            }
            foreach (var (node, rate, size) in new[] { ("Hopper", 2.5f, 5f), ("Screen", 3f, 6f), ("CrusherDischarge", 1.5f, 4f) })
            {
                var at = FleetRig.Find(plant, node);
                if (at == null) continue;
                var h = QuarryEffects.Emitter(at, "Haze", Vector3.zero, effects.dust, Max(24));
                QuarryEffects.AsDust(h, haze, new Vector2(size * 0.6f, size), new Vector2(5f, 8f), 0.22f, 0.04f);
                QuarryEffects.Box(h, new Vector3(3f, 1f, 3f));
                QuarryEffects.Rate(h, budget * rate);
                systems.Add(h);
            }
            foreach (var r in plant.GetComponentsInChildren<Renderer>(true))
            {
                var mats = r.sharedMaterials;
                for (int i = 0; i < mats.Length; i++)
                    if (mats[i] != null && mats[i].name.StartsWith("M_Ore_Belt", System.StringComparison.Ordinal))
                        belts.Add((r, i));
            }
            block ??= new MaterialPropertyBlock();
            flywheelLeft = FleetRig.Find(plant, "FlywheelLeft");
            flywheelRight = FleetRig.Find(plant, "FlywheelRight");
            screenBox = FleetRig.Find(plant, "ScreenBox");
            if (screenBox != null) screenRest = screenBox.localPosition;
            SetBelts(clock);
        }

        void SetBelts(float t)
        {
            float u = Mathf.Repeat(t * beltSpeed / beltPeriod, 1f);
            foreach (var (r, slot) in belts)
            {
                if (r == null) continue;
                r.GetPropertyBlock(block, slot);
                block.SetVector(BaseST, new Vector4(1f, 1f, -u, 0f));
                r.SetPropertyBlock(block, slot);
            }
        }

        void Update()
        {
            if (Application.isPlaying) Step(Time.deltaTime, false);
        }

        // built on first use, once the terrain the streams fall onto exists
        void EnsureBuilt()
        {
            if (holder == null && plant != null && effects != null && (terrain == null || terrain.Built)) Build();
        }

        public void ClearParticles()
        {
            foreach (var ps in systems)
                if (ps != null) ps.Clear(true);
        }

        public void Step(float dt, bool simulate)
        {
            EnsureBuilt();
            clock += dt;
            SetBelts(clock);
            // the flywheels turn about their shaft (X); the screen box shakes along and across its deck
            var spin = Quaternion.Euler(Mathf.Repeat(clock * flywheelSpeed * Mathf.Rad2Deg, 360f), 0f, 0f);
            if (flywheelLeft != null) flywheelLeft.localRotation = spin;
            if (flywheelRight != null) flywheelRight.localRotation = spin;
            if (screenBox != null)
            {
                float ph = clock * screenShake.y * Mathf.PI * 2f;
                screenBox.localPosition = screenRest + screenShake.x * new Vector3(0f, Mathf.Sin(ph), Mathf.Cos(ph));
            }
            foreach (var ps in systems) QuarryEffects.Advance(ps, dt, simulate);
        }
    }
}

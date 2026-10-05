// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using UnityEngine;

namespace DeviceChain.Sitepulse.Visuals
{
    /// <summary>
    /// Plays the 18-machine preview choreography that <c>ArtSource/terrain/quarry_fleet.py</c>
    /// writes, on the quarry terrain: haulers on the haul loop, loaders and dozers working.
    ///
    /// This is a visual stand-in so the scene shows a working site; it is local, scripted motion,
    /// not the site simulation and not platform data. Each machine is grounded on the terrain
    /// every frame (height, pitch and roll from four samples under its footprint) and its rig is
    /// driven from the recorded implement angles and the distance it travels.
    ///
    /// Machines are spawned when the component is enabled, in the Editor too, and are never
    /// saved with the scene. In the Editor, <see cref="Seek"/> poses the fleet at a given time.
    /// </summary>
    [ExecuteAlways]
    public sealed class QuarryFleetPreview : MonoBehaviour
    {
        public TextAsset choreography;
        public QuarryTerrain terrain;
        public GameObject dozer, dozerLod1, loader, loaderLod1, hauler, haulerLod1;
        [Tooltip("Dust, exhaust, beacons and falling material (cosmetic). None: no effects.")]
        public QuarryEffects effects;
        [Tooltip("A Sitepulse/Machine Lit material with the site's dust settings. Each machine draws with "
                 + "copies of it carrying its own glTF colours, so that its dust can follow its own ground. "
                 + "None: the imported materials, clean.")]
        public Material machineMaterial;
        [Tooltip("Seconds into the choreography (Edit mode pose; the start time in Play mode).")]
        public float time;
        public float timeScale = 1f;

        [Serializable]
        sealed class Choreography
        {
            public float dt;
            public Track[] tracks;
            public Machine[] machines;
        }

        [Serializable]
        sealed class Track
        {
            public string kind;
            public float period;
            public float[] data;
        }

        [Serializable]
        sealed class Machine
        {
            public string id;
            public string kind;
            public int track;
            public float offset;
        }

        const int Stride = 8;   // x, z, heading, travel, p1, p2, steer, flag

        sealed class Unit
        {
            public MachineRig rig;
            public MachineEffects effects;
            public Track track;
            public float offset, travel;
            public bool posed;
            public float halfLength, halfWidth;
            public Material[] dusty = Array.Empty<Material>();
            public float dustGround = float.NaN;
        }

        static readonly int DustGround = Shader.PropertyToID("_DustGround");
        static readonly string[] GltfColors = { "baseColorFactor", "emissiveFactor" };
        static readonly string[] GltfFloats = { "metallicFactor", "roughnessFactor" };

        /// <summary>Draw a machine with its own copies of <see cref="machineMaterial"/>, carrying
        /// the colours, maps and finishes of the materials it was imported with.</summary>
        Material[] Dirty(GameObject go)
        {
            if (machineMaterial == null) return Array.Empty<Material>();
            var copies = new Dictionary<Material, Material>();
            foreach (var r in go.GetComponentsInChildren<Renderer>(true))
            {
                var mats = r.sharedMaterials;
                for (int i = 0; i < mats.Length; i++)
                {
                    var src = mats[i];
                    if (src == null) continue;
                    if (!copies.TryGetValue(src, out var m))
                    {
                        m = new Material(machineMaterial) { name = src.name + " (dust)", hideFlags = HideFlags.DontSave };
                        foreach (var c in GltfColors)
                            if (src.HasProperty(c)) m.SetColor(c, src.GetColor(c));
                        foreach (var f in GltfFloats)
                            if (src.HasProperty(f)) m.SetFloat(f, src.GetFloat(f));
                        if (src.HasProperty("baseColorTexture"))
                        {
                            m.SetTexture("baseColorTexture", src.GetTexture("baseColorTexture"));
                            m.SetTextureScale("baseColorTexture", src.GetTextureScale("baseColorTexture"));
                            m.SetTextureOffset("baseColorTexture", src.GetTextureOffset("baseColorTexture"));
                        }
                        if (!src.IsKeywordEnabled("_EMISSIVE")) m.SetColor("emissiveFactor", Color.black);
                        copies.Add(src, m);
                    }
                    mats[i] = m;
                }
                r.sharedMaterials = mats;
            }
            var list = new Material[copies.Count];
            copies.Values.CopyTo(list, 0);
            return list;
        }

        readonly List<Unit> units = new List<Unit>();
        Choreography data;

        public int Count => units.Count;

        /// <summary>The machines, as spawned.</summary>
        public IEnumerable<MachineRig> Machines
        {
            get
            {
                foreach (var u in units)
                    if (u.rig != null) yield return u.rig;
            }
        }

        /// <summary>The period (s) of the track machine <paramref name="id"/> plays, or 0.</summary>
        public float CycleOf(string id)
        {
            foreach (var u in units)
                if (u.rig != null && u.rig.name == id) return u.track.period;
            return 0f;
        }

        /// <summary>The haul loop's cycle time (s): the period of the first hauler's track.</summary>
        public float HaulCycle
        {
            get
            {
                foreach (var u in units)
                    if (u.rig != null && u.rig.Kind == MachineKind.Hauler) return u.track.period;
                return 0f;
            }
        }

        void OnEnable() => Spawn();

        void OnDisable() => Despawn();

        void Spawn()
        {
            Despawn();
            if (choreography == null) return;
            data = JsonUtility.FromJson<Choreography>(choreography.text);
            foreach (var m in data.machines)
            {
                var kind = (MachineKind)Enum.Parse(typeof(MachineKind), m.kind);
                var (lod0, lod1) = kind switch
                {
                    MachineKind.Dozer => (dozer, dozerLod1),
                    MachineKind.Loader => (loader, loaderLod1),
                    _ => (hauler, haulerLod1),
                };
                if (lod0 == null || lod1 == null) continue;
                var go = FleetRig.BuildMergedLod(lod0, lod1, m.id, out var error);
                if (go == null)
                {
                    Debug.LogError($"[quarry-fleet] {m.id}: {error}");
                    continue;
                }
                foreach (var t in go.GetComponentsInChildren<Transform>(true))
                    t.gameObject.hideFlags = HideFlags.DontSave;
                go.transform.SetParent(transform, false);
                var dusty = Dirty(go);
                var rig = go.AddComponent<MachineRig>();
                rig.Bind(kind);
                // ground contact patch: track length x gauge, or wheelbase x track width
                var (hl, hw) = kind switch
                {
                    MachineKind.Dozer => (1.6f, 1.04f),
                    MachineKind.Loader => (1.65f, 1.15f),
                    _ => (2.1f, 2.2f),
                };
                MachineEffects fx = null;
                if (effects != null)
                {
                    fx = go.AddComponent<MachineEffects>();
                    fx.Bind(rig, effects);
                }
                units.Add(new Unit { rig = rig, effects = fx, track = data.tracks[m.track], offset = m.offset, halfLength = hl, halfWidth = hw, dusty = dusty });
            }
            Seek(time);
        }

        void Despawn()
        {
            foreach (var u in units)
            {
                if (u.rig != null) DestroyImmediate(u.rig.gameObject);
                foreach (var m in u.dusty) DestroyImmediate(m);
            }
            units.Clear();
        }

        void Update()
        {
            if (!Application.isPlaying) return;
            time += Time.deltaTime * timeScale;
            Seek(time);
            foreach (var u in units)
                if (u.effects != null) u.effects.Step(Time.deltaTime * timeScale, false);
        }

        /// <summary>
        /// Edit mode: play the scene from <paramref name="from"/> to <paramref name="to"/> seconds in
        /// fixed steps, simulating every effect as it goes, so that a still taken at the end shows
        /// dust and falling rock with the history they would have in play.
        /// </summary>
        public void Advance(float from, float to, float step = 1f / 30f)
        {
            foreach (var e in QuarryEffects.Active) e.ClearParticles();
            for (float t = from; t < to + step * 0.5f; t += step)
            {
                Seek(t);
                foreach (var e in QuarryEffects.Active.ToArray()) e.Step(step, true);
            }
        }

        /// <summary>Pose every machine at <paramref name="t"/> seconds into the choreography.</summary>
        public void Seek(float t)
        {
            time = t;
            if (data == null || terrain == null || !terrain.Built) return;
            foreach (var u in units)
            {
                var tr = u.track;
                int frames = tr.data.Length / Stride;
                float ft = Mathf.Repeat(t - u.offset, tr.period) / data.dt;
                int i = Mathf.Min((int)ft, frames - 1), j = (i + 1) % frames;
                float w = ft - (int)ft;
                float V(int k) => Mathf.Lerp(tr.data[i * Stride + k], tr.data[j * Stride + k], w);
                float x = V(0), z = V(1);
                float heading = tr.data[i * Stride + 2] + Mathf.DeltaAngle(tr.data[i * Stride + 2], tr.data[j * Stride + 2]) * w;
                float travel = j == 0 ? tr.data[i * Stride + 3] : V(3);
                Place(u, x, z, heading);
                var rig = u.rig;
                if (u.posed)
                {
                    float d = travel - u.travel;
                    if (Mathf.Abs(d) < 50f) rig.AddTravel(d);   // a jump means the track looped
                }
                u.travel = travel;
                u.posed = true;
                switch (rig.Kind)
                {
                    case MachineKind.Dozer:
                        rig.bladeArm = V(4);
                        rig.ripper = V(5);
                        break;
                    case MachineKind.Loader:
                        rig.boom = V(4);
                        rig.bucket = V(5);
                        rig.steer = V(6);
                        break;
                    case MachineKind.Hauler:
                        rig.dump = V(4);
                        rig.steer = V(6);
                        rig.loaded = tr.data[i * Stride + 7] > 0.5f;
                        break;
                }
                if (!Application.isPlaying) rig.Apply();
            }
        }

        /// <summary>Sit the machine on the ground: height and attitude from four samples under it.</summary>
        void Place(Unit u, float x, float z, float heading)
        {
            var yaw = Quaternion.Euler(0f, heading, 0f);
            Vector3 f = yaw * Vector3.forward, r = yaw * Vector3.right, c = new Vector3(x, 0f, z);
            float H(Vector3 p) => terrain.HeightAt(p.x, p.z);
            float hf = H(c + f * u.halfLength), hb = H(c - f * u.halfLength);
            float hr = H(c + r * u.halfWidth), hl = H(c - r * u.halfWidth);
            float pitch = -Mathf.Atan2(hf - hb, 2f * u.halfLength) * Mathf.Rad2Deg;
            float roll = Mathf.Atan2(hr - hl, 2f * u.halfWidth) * Mathf.Rad2Deg;
            float y = Mathf.Max((hf + hb) * 0.5f, (hl + hr) * 0.5f);
            u.rig.transform.SetPositionAndRotation(new Vector3(x, y, z), Quaternion.Euler(pitch, heading, roll));
            if (!(Mathf.Abs(y - u.dustGround) <= 0.01f))          // NaN: never set
            {
                u.dustGround = y;
                foreach (var m in u.dusty) m.SetFloat(DustGround, y);
            }
        }
    }
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Sitepulse.Tasks;
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

        sealed class Unit
        {
            public MachineRig rig;
            public MachineEffects effects;
            public Track track;
            public float travel;

            /// <summary>The machine's routine track as it is played: its clock, its rate and its frames (the class the EditMode bodies play theirs through).</summary>
            public TrackPlayer player;
            public bool posed;
            public float halfLength, halfWidth;
            public bool detached;
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
                units.Add(new Unit { rig = rig, effects = fx, track = data.tracks[m.track], player = new TrackPlayer(data.tracks[m.track].data, data.dt, data.tracks[m.track].period, m.offset), halfLength = hl, halfWidth = hw, dusty = dusty });
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
            Play(Time.deltaTime * timeScale);
        }

        /// <summary>
        /// One frame of play: the scene clock moves on by <paramref name="step"/> seconds and every machine on its track is posed
        /// at its own clock, which runs at the rate the task layer set for it (<see cref="SetTrackRate"/>).
        /// </summary>
        public void Play(float step)
        {
            time += step;
            // a track held back plays less of itself: its clock is put back by what it did not play
            foreach (var u in units)
                if (!u.detached) u.player.Advance(step);
            Seek(time);
            foreach (var u in units)
                if (u.effects != null) u.effects.Step(step, false);
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

        // ---- task control: a machine taken off its track is driven by someone else (the task layer) -----------

        Unit Find(string id)
        {
            foreach (var u in units)
                if (u.rig != null && u.rig.name == id) return u;
            return null;
        }

        /// <summary>Whether the machine is on its routine track (an unknown id answers false).</summary>
        public bool IsOnTrack(string id) => Find(id) is Unit u && !u.detached;

        /// <summary>
        /// Takes the machine off its track at the pose it has now, with its implements stowed for travel:
        /// from here on only <see cref="Drive"/> moves it, until <see cref="Attach"/> puts it back.
        /// </summary>
        public void Detach(string id)
        {
            var u = Find(id);
            if (u == null || u.detached) return;
            u.detached = true;
            u.player.Detach();
            var rig = u.rig;
            switch (rig.Kind)
            {
                case MachineKind.Dozer:
                    rig.bladeArm = FleetRig.DozerBladeRaised;
                    rig.ripper = 0f;
                    break;
                case MachineKind.Loader:
                    rig.boom = 0f;
                    rig.bucket = -4f;
                    break;
                case MachineKind.Hauler:
                    rig.dump = 0f;
                    break;
            }
        }

        /// <summary>Puts a detached machine on the ground at a place, facing a way, having moved <paramref name="distance"/> metres (wheels turn by it).</summary>
        public void Drive(string id, float x, float z, float heading, float distance, float steer)
        {
            var u = Find(id);
            if (u == null || !u.detached || terrain == null || !terrain.Built) return;
            Place(u, x, z, heading);
            if (distance != 0f) u.rig.AddTravel(distance);
            if (u.rig.Kind != MachineKind.Dozer) u.rig.steer = steer;
        }

        /// <summary>
        /// Replay: puts a detached machine exactly where and how a recording says it was drawn: its position and attitude as
        /// recorded (nothing is re-grounded on the terrain), its implement angles, steering and load, and its wheels turned by
        /// <paramref name="distance"/> metres since the last call.
        /// </summary>
        public bool PoseRecorded(string id, Vector3 position, float heading, float pitch, float roll, float p1, float p2, float steer, bool loaded, float distance)
        {
            var u = Find(id);
            if (u == null || !u.detached) return false;
            var rig = u.rig;
            rig.transform.SetPositionAndRotation(position, Quaternion.Euler(pitch, heading, roll));
            if (!(Mathf.Abs(position.y - u.dustGround) <= 0.01f))
            {
                u.dustGround = position.y;
                foreach (var m in u.dusty) m.SetFloat(DustGround, position.y);
            }

            if (distance != 0f) rig.AddTravel(distance);
            switch (rig.Kind)
            {
                case MachineKind.Dozer:
                    rig.bladeArm = p1;
                    rig.ripper = p2;
                    break;
                case MachineKind.Loader:
                    rig.boom = p1;
                    rig.bucket = p2;
                    rig.steer = steer;
                    break;
                case MachineKind.Hauler:
                    rig.dump = p1;
                    rig.steer = steer;
                    rig.loaded = loaded;
                    break;
            }

            return true;
        }

        /// <summary>How fast the machine's routine track plays, from 0 (held where it is) to 1 (the track's own pace; see <see cref="TrackPlayer.Rate"/>). A machine that is off its track ignores it.</summary>
        public void SetTrackRate(string id, float rate)
        {
            var u = Find(id);
            if (u != null && !u.detached) u.player.SetRate(rate);
        }

        /// <summary>The place on the machine's own track nearest to a point: its position, heading and how far into the loop it is.</summary>
        public bool TryNearestTrackPoint(string id, float x, float z, out Vector2 position, out float heading, out float trackSeconds)
        {
            position = default;
            heading = 0f;
            trackSeconds = 0f;
            var u = Find(id);
            if (u == null || data == null) return false;
            if (!u.player.TryNearest(x, z, out var p)) return false;
            position = new Vector2((float)p.X, (float)p.Z);
            heading = (float)p.HeadingDegrees;
            trackSeconds = (float)p.TrackSeconds;
            return true;
        }

        /// <summary>Puts a detached machine back on its track at a place in the loop (as <see cref="TryNearestTrackPoint"/> gave it): it resumes routine work from there.</summary>
        public void Attach(string id, float trackSeconds)
        {
            var u = Find(id);
            if (u == null || !u.detached) return;
            u.player.Attach(time, trackSeconds);
            u.posed = false;
            u.detached = false;
            Seek(time);
        }

        /// <summary>The machine's pose in the site's metres: position and heading (degrees clockwise from north).</summary>
        public bool TryGetPose(string id, out float x, out float z, out float heading)
        {
            var u = Find(id);
            if (u == null) { x = z = heading = 0f; return false; }
            var t = u.rig.transform;
            x = t.position.x;
            z = t.position.z;
            heading = t.eulerAngles.y;
            return true;
        }

        /// <summary>Pose every machine at <paramref name="t"/> seconds into the choreography.</summary>
        public void Seek(float t)
        {
            time = t;
            if (data == null || terrain == null || !terrain.Built) return;
            foreach (var u in units)
            {
                if (u.detached) continue;
                var f = u.player.SampleAt(t);
                Place(u, f.X, f.Z, f.Heading);
                var rig = u.rig;
                if (u.posed)
                {
                    float d = f.Travel - u.travel;
                    if (Mathf.Abs(d) < 50f) rig.AddTravel(d);   // a jump means the track looped
                }
                u.travel = f.Travel;
                u.posed = true;
                switch (rig.Kind)
                {
                    case MachineKind.Dozer:
                        rig.bladeArm = f.P1;
                        rig.ripper = f.P2;
                        break;
                    case MachineKind.Loader:
                        rig.boom = f.P1;
                        rig.bucket = f.P2;
                        rig.steer = f.Steer;
                        break;
                    case MachineKind.Hauler:
                        rig.dump = f.P1;
                        rig.steer = f.Steer;
                        rig.loaded = f.Loaded;
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

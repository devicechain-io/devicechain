// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Text.RegularExpressions;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>
    /// The level-of-detail decision, with no engine in it. Each set (a building, a vehicle) has a centre and a radius; the nearest ones to the
    /// active camera are NEAR. A set enters at <see cref="EnterM"/> and leaves at <see cref="LeaveM"/> (hysteresis, so a camera sitting on the
    /// edge cannot flicker it), no more than <see cref="MaxNear"/> are near at once, and the work allocates nothing after construction.
    /// The switch only ever ADDS parts to what the far level already draws, so there is no hole to open and nothing to pop but the detail.
    /// </summary>
    public sealed class DetailSwitch
    {
        public const float EnterM = 110f, LeaveM = 130f;
        public const int MaxNear = 25;
        /// <summary>A set already near wins a tie for the last slots by this much, so the cap itself cannot flicker either.</summary>
        public const float StickyM = 6f;
        /// <summary>How often the decision is retaken while the camera is still, and how far it must move to retake it at once.</summary>
        public const int EveryFrames = 4;
        public const float MovedM = 1f;

        float[] cx, cy, cz, radius, key;
        bool[] near, active, next;
        int[] order;
        int count;
        float lastX, lastY, lastZ; long lastFrame; bool taken;

        public DetailSwitch(int capacity = 64)
        {
            Grow(Math.Max(4, capacity));
        }

        void Grow(int size)
        {
            Array.Resize(ref cx, size); Array.Resize(ref cy, size); Array.Resize(ref cz, size); Array.Resize(ref radius, size); Array.Resize(ref key, size);
            Array.Resize(ref near, size); Array.Resize(ref active, size); Array.Resize(ref next, size); Array.Resize(ref order, size);
        }

        public int Count => count;

        /// <summary>Adds a set and returns its index. Build time only: this may allocate.</summary>
        public int Add(float x, float y, float z, float radiusM)
        {
            if (count == cx.Length) Grow(count * 2);
            cx[count] = x; cy[count] = y; cz[count] = z; radius[count] = radiusM; active[count] = true; near[count] = false;
            return count++;
        }

        /// <summary>Moves a set (the truck, a boat on the swell). Cheap; takes effect at the next <see cref="Update"/>.</summary>
        public void Move(int i, float x, float y, float z) { cx[i] = x; cy[i] = y; cz[i] = z; }

        /// <summary>An inactive set (a charger's car while no session shows it) is never near.</summary>
        public void SetActive(int i, bool on) { active[i] = on; }

        public bool IsNear(int i) => near[i];

        public int NearCount
        {
            get { var n = 0; for (var i = 0; i < count; i++) if (near[i]) n++; return n; }
        }

        /// <summary>The camera's distance to the set's nearest edge: centre distance less the radius, never below zero.</summary>
        public float Distance(int i, float x, float y, float z)
        {
            float dx = cx[i] - x, dy = cy[i] - y, dz = cz[i] - z;
            var d = (float)Math.Sqrt(dx * dx + dy * dy + dz * dz) - radius[i];
            return d < 0 ? 0 : d;
        }

        /// <summary>
        /// True when the decision should be retaken: the camera moved by <see cref="MovedM"/>, or <see cref="EveryFrames"/> frames passed.
        /// It does not retake it; <see cref="Update"/> does, and records the frame and place.
        /// </summary>
        public bool Due(long frame, float x, float y, float z)
        {
            if (!taken) return true;
            if (frame - lastFrame >= EveryFrames) return true;
            float dx = x - lastX, dy = y - lastY, dz = z - lastZ;
            return dx * dx + dy * dy + dz * dz > MovedM * MovedM;
        }

        /// <summary>Retakes the decision for a camera at the given place. Returns how many sets changed level.</summary>
        public int Update(long frame, float x, float y, float z)
        {
            taken = true; lastFrame = frame; lastX = x; lastY = y; lastZ = z;
            var candidates = 0;
            for (var i = 0; i < count; i++)
            {
                next[i] = false;
                if (!active[i]) continue;
                var d = Distance(i, x, y, z);
                if (d >= (near[i] ? LeaveM : EnterM)) continue;
                next[i] = true;
                // the rank: nearest first, a set already near a little ahead, the index to break exact ties the same way every time
                key[candidates] = d - (near[i] ? StickyM : 0f) + i * 1e-5f;
                order[candidates] = i;
                candidates++;
            }
            if (candidates > MaxNear)
            {
                Array.Sort(key, order, 0, candidates);
                for (var k = MaxNear; k < candidates; k++) next[order[k]] = false;
            }
            var changed = 0;
            for (var i = 0; i < count; i++)
            {
                if (near[i] != next[i]) changed++;
                near[i] = next[i];
            }
            return changed;
        }
    }

    /// <summary>
    /// Which sets any story camera can come within reach of, so those can be built at load rather than on the frame the camera cuts to them.
    /// The rest are built the first time something comes near, which only costs a one-off hitch for a free camera.
    /// </summary>
    public static class DetailCandidates
    {
        /// <summary>The margin on every pose camera: the cameras are tuned by hand and a pose may move a good way before this is revisited.</summary>
        public const float PoseMarginM = 20f;

        static readonly string[] Groups = { "pump-north", "pump-east", "pump-basin", "chargers-east", "chargers-basin", "lamp-east", "gauge-north", "gauge-east",
            "terminal", "north-quay", "charger-north" };
        static readonly string[] Suffixes = { "kiosk-ext", "well", "well-locked", "pair", "close", "wide", "promenade", "insert" };

        /// <summary>Every pose name the presentation map authors by pattern, plus the fixed ones. A name the map does not know is skipped by the caller.</summary>
        public static IReadOnlyList<string> PoseNames()
        {
            var names = new List<string> { "pose:home", "pose:home:portrait", "pose:night-home", "pose:storm-home", "pose:east-quay:mouth-wide", "pose:plain:wide",
                "pose:truck:chase", "pose:depot:wide" };
            foreach (var g in Groups) foreach (var s in Suffixes) { var n = "pose:" + g + ":" + s; if (!names.Contains(n)) names.Add(n); }
            return names;
        }

        static readonly Regex PoseToken = new Regex("pose:[a-z0-9:-]+", RegexOptions.CultureInvariant);

        /// <summary>The pose names a storyboard document mentions, so a pose it adds is a candidate without anyone editing a list.</summary>
        public static IReadOnlyList<string> PoseNamesIn(string json)
        {
            var found = new List<string>();
            if (string.IsNullOrEmpty(json)) return found;
            foreach (Match m in PoseToken.Matches(json)) if (!found.Contains(m.Value)) found.Add(m.Value);
            return found;
        }

        /// <summary>True when a camera at (x, y, z) could, within the margin, have this set near: it is a build-at-load candidate.</summary>
        public static bool InReach(float setX, float setY, float setZ, float setRadius, float x, float y, float z, float margin = PoseMarginM)
        {
            float dx = setX - x, dy = setY - y, dz = setZ - z;
            return Math.Sqrt(dx * dx + dy * dy + dz * dz) - setRadius < DetailSwitch.LeaveM + margin;
        }

        /// <summary>
        /// Whether a set is a candidate for any of the story cameras. A camera orbits and dollies about its look point, so the look point
        /// (at the leave range) counts as well as the camera itself (with the margin).
        /// </summary>
        public static bool Reachable(float setX, float setY, float setZ, float setRadius, IReadOnlyList<NearVec> cameras, IReadOnlyList<NearVec> looks)
        {
            foreach (var c in cameras) if (InReach(setX, setY, setZ, setRadius, c.X, c.Y, c.Z)) return true;
            foreach (var l in looks) if (InReach(setX, setY, setZ, setRadius, l.X, l.Y, l.Z, 0)) return true;
            return false;
        }
    }
}

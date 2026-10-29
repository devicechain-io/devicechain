// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Towerpulse.DevicePlane;
using DeviceChain.Towerpulse.Model;
using DeviceChain.Towerpulse.Plane;
using DeviceChain.Towerpulse.Story;
using UnityEngine;

namespace DeviceChain.Towerpulse.Visuals
{
    /// <summary>
    /// The campus in motion. It reads what the plane's devices REPORT (the same readings they publish) and turns it into colour and movement: a zone's
    /// glass volume is tinted by its reported temperature against its reported setpoint, people appear to match its reported occupancy, fans turn at the
    /// reported speed and a chiller's lamp says running, stopped or not reporting. It decides nothing about the building and calculates nothing.
    ///
    /// The scene is built once by <c>CampusScene.Build</c>: this component only finds the pieces it moves, by name, so a renamed node is a loud error and not a
    /// silently static building.
    /// </summary>
    [DefaultExecutionOrder(50)]
    public sealed class CampusView : MonoBehaviour
    {
        /// <summary>The property the glTF material takes its colour from (glTFast's shader graph names it so).</summary>
        public const string BaseColorProperty = "baseColorFactor", EmissiveProperty = "emissiveFactor";
        public const string CampusRootName = "Campus";
        public const double GlowIntensity = 1.6;
        public const float RefreshHz = 10f;
        public const float FanRampDegPerS2 = 540f;

        sealed class ZoneRig
        {
            public ZonePlan Plan;
            public Renderer[] Tint;
            public Transform[] Slots;
            public int ShownPeople = -1;
            public Color LastColour;
            public PlaneDevice Device;
        }

        sealed class FanRig
        {
            public Transform Node;
            public Quaternion Rest;
            public double Angle, Rate;
        }

        sealed class UnitRig
        {
            public UnitPlan Plan;
            public FanRig[] Fans;
            public Renderer[] Lamp;
            public Light LampLight;
            public PlaneDevice Device;
            public Transform Node;
            public LampState Lamps = LampState.NotReporting;
            public double TargetRate;
        }

        [SerializeField] bool cutaway = true;

        readonly List<ZoneRig> zones = new List<ZoneRig>();
        readonly List<UnitRig> ahus = new List<UnitRig>();
        readonly List<UnitRig> chillers = new List<UnitRig>();
        readonly Dictionary<string, Transform> anchors = new Dictionary<string, Transform>();
        MaterialPropertyBlock block;
        TowerpulseLiveApp app;
        CampusPlan plan;
        int generation = -1;
        float nextRefresh;
        Transform root;

        /// <summary>Why the view could not bind (a missing node), or null. Shown by the story UI; never swallowed.</summary>
        public string Problem { get; private set; }

        public LampState ChillerLamp(string layoutId)
        {
            foreach (var c in chillers) if (c.Plan.Id == layoutId) return c.Lamps;
            return LampState.NotReporting;
        }

        /// <summary>The world position of a zone's card anchor (the model's own <c>Anchor_</c> node), or false when the zone is unknown.</summary>
        public bool TryAnchor(string layoutId, out Vector3 world)
        {
            foreach (var z in zones)
                if (z.Plan.Id == layoutId && anchors.TryGetValue(layoutId, out var t)) { world = t.position; return true; }
            world = default;
            return false;
        }

        /// <summary>A unit's world position (its lamp when it has one), for placing its card.</summary>
        public bool TryUnitPosition(string layoutId, out Vector3 world)
        {
            foreach (var u in chillers) if (u.Plan.Id == layoutId) { world = u.Node.position + Vector3.up * 1.5f; return true; }
            world = default;
            return false;
        }

        public void Bind(TowerpulseLiveApp liveApp, CampusPlan campus)
        {
            app = liveApp;
            plan = campus;
            var found = GameObject.Find(CampusRootName);
            root = found != null ? found.transform : null;
            block = new MaterialPropertyBlock();
            zones.Clear(); ahus.Clear(); chillers.Clear(); anchors.Clear();
            Problem = null;
            try
            {
                if (root == null) throw new InvalidOperationException("the scene has no \"" + CampusRootName + "\" object: rebuild it from the Towerpulse menu");
                Find();
            }
            catch (Exception e)
            {
                Problem = e.Message;
                Debug.LogError("[towerpulse] the campus view could not bind: " + e.Message);
            }
            if (cutaway) ApplyCutaway();
        }

        void Awake()
        {
            // a scene saved with the view already on it binds itself; the story app binds it when it is added at run time
        }

        static Transform Deep(Transform from, string name)
        {
            if (from.name == name) return from;
            for (int i = 0; i < from.childCount; i++)
            {
                var hit = Deep(from.GetChild(i), name);
                if (hit != null) return hit;
            }
            return null;
        }

        Transform Need(Transform from, string name, string what)
        {
            var t = Deep(from, name);
            if (t == null) throw new InvalidOperationException(what + ": no node named \"" + name + "\" under \"" + from.name + "\"");
            return t;
        }

        void Find()
        {
            foreach (var z in plan.Zones)
            {
                var b = Need(root, z.Building, "zone " + z.Id);
                var node = Need(b, z.Node, "zone " + z.Id);
                anchors[z.Id] = Need(b, z.Anchor, "zone " + z.Id);
                var rig = new ZoneRig { Plan = z, Tint = node.GetComponentsInChildren<Renderer>(true) };
                if (rig.Tint.Length == 0) throw new InvalidOperationException("zone " + z.Id + ": the node \"" + z.Node + "\" has no mesh to tint");
                var people = Deep(root, "People_" + z.Id);
                if (people != null)
                {
                    rig.Slots = new Transform[people.childCount];
                    for (int i = 0; i < people.childCount; i++) rig.Slots[i] = people.GetChild(i);
                }
                else rig.Slots = new Transform[0];
                zones.Add(rig);
            }
            foreach (var u in plan.RooftopUnits) ahus.Add(Unit(u, false));
            foreach (var u in plan.Chillers) chillers.Add(Unit(u, true));
        }

        UnitRig Unit(UnitPlan u, bool lamp)
        {
            var t = Need(root, u.Id, "unit " + u.Id);
            var rig = new UnitRig { Plan = u, Node = t, Fans = new FanRig[u.FanNodes.Count] };
            for (int i = 0; i < rig.Fans.Length; i++)
            {
                var n = Need(t, u.FanNodes[i], "unit " + u.Id);
                rig.Fans[i] = new FanRig { Node = n, Rest = n.localRotation };
            }
            if (lamp)
            {
                var l = Need(t, u.LampNode, "unit " + u.Id);
                rig.Lamp = l.GetComponentsInChildren<Renderer>(true);
                var glow = new GameObject("LampGlow");
                glow.transform.SetParent(l, false);
                glow.transform.localPosition = new Vector3(0, 0.25f, 0);
                rig.LampLight = glow.AddComponent<Light>();
                rig.LampLight.type = LightType.Point;
                rig.LampLight.range = 7f;
                rig.LampLight.shadows = LightShadows.None;
            }
            return rig;
        }

        // Hero cutaway: the south and east faces are taken away so the camera, standing south-east, sees into the rooms.
        void ApplyCutaway()
        {
            foreach (var b in plan.Buildings)
            {
                var bt = Deep(root, b.Id);
                if (bt == null) continue;
                foreach (var n in new[] { "Facade_South", "Facade_East" })
                {
                    var f = Deep(bt, n);
                    if (f != null) f.gameObject.SetActive(false);
                }
            }
        }

        void Resolve()
        {
            var plane = app.Plane;
            generation = app.Generation;
            foreach (var z in zones) z.Device = OfflinePreview.ByLayout(plane, z.Plan.Id);
            foreach (var u in ahus) u.Device = OfflinePreview.ByLayout(plane, u.Plan.Id);
            foreach (var u in chillers) u.Device = OfflinePreview.ByLayout(plane, u.Plan.Id);
            foreach (var z in zones) z.ShownPeople = -1;
        }

        void LateUpdate()
        {
            if (app == null || app.Plane == null || Problem != null) return;
            if (generation != app.Generation) Resolve();
            if (Time.unscaledTime >= nextRefresh)
            {
                nextRefresh = Time.unscaledTime + 1f / RefreshHz;
                Refresh();
            }
            Spin(Time.unscaledDeltaTime);
            Lamps(Time.unscaledTime);
        }

        /// <summary>Applies the current readings at once (a still, a seek). Safe to call any time after <see cref="Bind"/>.</summary>
        public void RefreshNow()
        {
            if (app == null || app.Plane == null || Problem != null) return;
            if (generation != app.Generation) Resolve();
            Refresh();
            Lamps(0f); // a still shows the lamps lit, not mid-blink
        }

        void Refresh()
        {
            var m = app.Plane.Model;
            foreach (var z in zones)
            {
                var r = Readings.For(m, z.Device);
                double temp = double.NaN, set = double.NaN, occ = 0;
                if (r != null)
                {
                    r.TryGetValue(Wire.Temp, out temp);
                    r.TryGetValue(Wire.Setpoint, out set);
                    r.TryGetValue(Wire.Occupancy, out occ);
                    if (!r.ContainsKey(Wire.Temp)) temp = double.NaN;
                    if (!r.ContainsKey(Wire.Setpoint)) set = double.NaN;
                }
                var c = TintRamp.Colour(temp, set);
                var col = new Color(c.R, c.G, c.B, c.A).linear;
                col.a = c.A;
                z.LastColour = col;
                block.Clear();
                block.SetColor(BaseColorProperty, col);
                block.SetColor(EmissiveProperty, new Color(c.R, c.G, c.B, 1f).linear * (float)(GlowIntensity * TintRamp.Glow(temp, set)));
                foreach (var rend in z.Tint) rend.SetPropertyBlock(block);

                int show = PeopleView.Visible(occ, z.Slots.Length);
                if (show != z.ShownPeople)
                {
                    z.ShownPeople = show;
                    for (int i = 0; i < z.Slots.Length; i++) z.Slots[i].gameObject.SetActive(i < show);
                }
            }
            foreach (var u in ahus)
            {
                var r = Readings.For(m, u.Device);
                double fan = 0;
                if (r != null) r.TryGetValue(Wire.Fan, out fan);
                u.TargetRate = FanSpin.AhuDegPerS(fan);
            }
            foreach (var u in chillers)
            {
                var r = Readings.For(m, u.Device);
                u.Lamps = LampLook.Of(r);
                u.TargetRate = FanSpin.ChillerFansDegPerS(u.Lamps == LampState.Running);
            }
        }

        void Spin(float dt)
        {
            foreach (var u in ahus) SpinUnit(u, dt);
            foreach (var u in chillers) SpinUnit(u, dt);
        }

        static void SpinUnit(UnitRig u, float dt)
        {
            foreach (var f in u.Fans)
            {
                f.Rate = FanSpin.Approach(f.Rate, u.TargetRate, FanRampDegPerS2, dt);
                f.Angle = (f.Angle + f.Rate * dt) % 360.0;
                f.Node.localRotation = f.Rest * Quaternion.AngleAxis((float)f.Angle, Vector3.up);
            }
        }

        void Lamps(float time)
        {
            foreach (var u in chillers)
            {
                var c = LampLook.Colour(u.Lamps);
                float b = (float)LampLook.Brightness(u.Lamps, time);
                var lin = new Color(c.R, c.G, c.B, 1f);
                block.Clear();
                block.SetColor(BaseColorProperty, lin.linear * Mathf.Lerp(0.15f, 1f, b));
                block.SetColor(EmissiveProperty, lin.linear * (b * 6f));
                foreach (var r in u.Lamp) r.SetPropertyBlock(block);
                u.LampLight.color = lin;
                u.LampLight.intensity = 6f * b;
            }
        }

        /// <summary>The colour a zone was last tinted with (what the renderer was handed), for tests and the zone card.</summary>
        public bool TryZoneColour(string layoutId, out Color colour)
        {
            foreach (var z in zones) if (z.Plan.Id == layoutId) { colour = z.LastColour; return true; }
            colour = default;
            return false;
        }

        public int PeopleShown(string layoutId)
        {
            foreach (var z in zones) if (z.Plan.Id == layoutId) return z.ShownPeople;
            return -1;
        }

        public double FanRate(string unitId)
        {
            foreach (var u in ahus) if (u.Plan.Id == unitId) return u.Fans[0].Rate;
            foreach (var u in chillers) if (u.Plan.Id == unitId) return u.Fans[0].Rate;
            return double.NaN;
        }
    }
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>The drawn ground plan comes only from PresentationMap. No manifest metres are drawn.</summary>
    public sealed class HarborWorld : IDisposable
    {
        public const float SeabedY = -6.5f;
        readonly PresentationMap map;
        readonly HarborMaterials mats;
        readonly List<AssetRig> rigs = new List<AssetRig>();
        readonly List<Transform> buoys = new List<Transform>();
        readonly List<Vector2> buoyRest = new List<Vector2>();
        public Transform Root { get; private set; }
        public IReadOnlyList<AssetRig> Rigs => rigs;
        public IReadOnlyList<Transform> Buoys => buoys;
        public IReadOnlyList<Vector2> BuoyRest => buoyRest;
        public Dictionary<string, List<Renderer>> Masks { get; } = new Dictionary<string, List<Renderer>>();
        public Dictionary<string, QuayFrame> Frames { get; } = new Dictionary<string, QuayFrame>();
        public AssetRig Truck => rigs.First(r => r.Placement.Device.Kind == DeviceKind.Truck);
        static Vector3 V(Ground p, float y = 0) => new Vector3((float)p.X, y, (float)p.Z);
        const float Deck = (float)SitePlan.DeckHeightM;
        HarborWorld(PresentationMap map, HarborMaterials mats) { this.map = map; this.mats = mats; }
        public static HarborWorld Build(PresentationMap map, HarborLayout layout, HarborMaterials mats, Transform parent)
        {
            var w = new HarborWorld(map, mats);
            w.Root = AssetRig.Node("Waterfront", parent, Vector3.zero);
            w.Ground(layout);
            QuayBuilder.Build(map, w.Root, mats);
            foreach (var token in map.Quays.Keys) w.Mark(token, w.Root.Find(token));
            foreach (var d in layout.Devices) w.Device(d, layout);
            // The mapped depot sits just south of the eastern breakwater. Give its yard
            // a real foundation and connect it by the mapped road causeway, never a floating slab.
            w.Slab("Depot headland", (float)map.Depot.X - 36, (float)map.Depot.X + 36,
                (float)map.Depot.Z - 26, (float)map.Depot.Z + 26, Deck, mats.Ground);
            BuildingBuilder.Depot(w.Root, V(map.Depot, Deck), mats);
            w.Town(); w.Ridge(); w.FloodPlain(); w.Breakwaters(); w.Floats();
            return w;
        }
        void Mark(string category, Transform t)
        {
            if (!Masks.TryGetValue(category, out var list)) Masks[category] = list = new List<Renderer>();
            list.AddRange(t.GetComponentsInChildren<Renderer>(true));
        }
        Transform Slab(string name, float x0, float x1, float z0, float z1, float top, Material mat, Transform parent = null)
            => new MeshKit().Slab(x0, x1, z0, z1, SeabedY, top).Spawn(name, parent ?? Root, mat).transform;
        void Ground(HarborLayout layout)
        {
            var inner = new TideField.Region(map.InnerWater);
            var north = new TideField.Region(map.Quays["hp-north-quay"]);
            var basin = new TideField.Region(map.Quays["hp-basin"]);
            var east = new TideField.Region(map.Quays["hp-east-quay"]);
            // HOME arithmetic: forward=(0,-.2326,.9726), focal=1080/(2*tan15)=2015 px.
            // At North Quay z=95 depth=526 m, 2.2 m of exposed rig is 8.4 px.
            // With up=(0,.9726,.2326), ridge (z=265,y=22) projects to normalized y=.700,
            // and (z=457,y=134) to y=1.000: .5 + .5*dot(up,p-camera)/(depth*tan15).
            // Terrace roofs replace sky while remaining behind all quay rigs.
            // The top ray at z=280 is y=129 m, so the church spire tops at 123 m.
            // Portrait forward=(0,-.3795,.9252), focal=1920/(2*tan26)=1968 px.
            // At Basin z=90 depth=449 m its horizontal half-width is only 123 m: the
            // prescribed 52 degree portrait pose cannot contain x=-155..-230 (see README).
            Slab("North hinterland", -1100, 1100, (float)north.MaxZ, 3000, Deck, mats.Ground);
            Slab("North west connection", (float)basin.MinX, (float)north.MinX, (float)basin.MaxZ, (float)north.MaxZ, Deck, mats.Ground);
            Slab("North east connection", (float)north.MaxX, (float)east.MaxX, (float)east.MaxZ, (float)north.MaxZ, Deck, mats.Ground);
            Slab("West shoulder", (float)basin.MaxX, (float)north.MinX, (float)inner.MaxZ, (float)basin.MaxZ, Deck, mats.Ground);
            Slab("West hinterland", -1100, (float)basin.MinX, (float)inner.MinZ, (float)north.MaxZ, Deck, mats.Ground);
            Slab("East hinterland", (float)east.MaxX, 1100, (float)inner.MinZ, (float)north.MaxZ, Deck, mats.Ground);
            Slab("Harbour floor", -9000, 9000, -9000, 3000, SeabedY, mats.Seabed);
            foreach (var token in map.Quays.Keys)
            {
                var f = new QuayFrame(map, token); Frames.Add(token, f);
                var t = f.Parent(Root, "ground:" + token);
                // Tile the quay top round each well's actual transformed disc. No opaque deck
                // may close a U1 cut-away. A small square aperture surrounds each round shell.
                var pumps = layout.Devices.Where(d => d.Quay == token && d.Kind == DeviceKind.Pump)
                    .Select(d => f.Local(V(map.Anchors[d.Token])) + new Vector3(9, 0, 6)).ToArray();
                var paving = new MeshKit();
                for (float x = -f.Length / 2; x < f.Length / 2; x += 2)
                    for (float z = -f.Depth; z < 0; z += 2)
                    {
                        var clear = pumps.Any(p => Math.Abs(p.x - (x + 1)) < 3.5 && Math.Abs(p.z - (z + 1)) < 3.5);
                        var center = f.World(new Vector3(x + 1, 0, z + 1));
                        var low = IsPlain(new Ground(center.x, center.z));
                        if (!clear) paving.Slab(x, Math.Min(x + 2, f.Length / 2), z, Math.Min(z + 2, 0), SeabedY, low ? Deck - .7f : Deck);
                    }
                paving.Spawn("Quay paving", t, mats.Paving);
                Slab("Wall", -f.Length / 2, f.Length / 2, -.8f, 0, Deck + .35f, mats.Stone, t);
                Slab("Promenade", -f.Length / 2, f.Length / 2, -7, -2, Deck + .015f, mats.Paving, t);
                Slab("Quay road", -f.Length / 2, f.Length / 2, -f.Depth + 2, -f.Depth + 9, Deck + .03f, mats.Asphalt, t);
                Mark(token, t);
            }
        }
        void Device(LayoutDevice d, HarborLayout layout)
        {
            var f = d.Kind == DeviceKind.Truck ? null : Frames[d.Quay];
            var at = map.Anchors[d.Token];
            var actual = new Placement(d, at, d.Kind == DeviceKind.Truck ? 0 : d.Quay == "hp-north-quay" ? 180 : d.Quay == "hp-east-quay" ? 270 : 90);
            var parent = AssetRig.Node("anchor:" + d.Token, Root, V(at));
            if (f != null) parent.localRotation = f.Rotation;
            var local = new Placement(d, new Ground(0, 0));
            var wallDistance = f == null ? 0 : -f.Local(V(at)).z;
            AssetRig r;
            switch (d.Kind)
            {
                case DeviceKind.Pump: r = PumpStationBuilder.Build(local, parent, mats, wallDistance); break;
                case DeviceKind.Gauge: case DeviceKind.LoraGauge: case DeviceKind.LegacyGauge:
                    r = GaugeBuilder.Build(local, parent, mats, layout);
                    // Keep the anchor, extend the board to the real wall face, and connect
                    // the sensor to it by a narrow catwalk. LoRa and legacy retain their pile.
                    var lateral = d.Quay == "hp-north-quay" ? 0 : d.Kind == DeviceKind.LoraGauge ? -10 : d.Kind == DeviceKind.LegacyGauge ? -7 : (d.Index - 2) * 3;
                    foreach (var name in new[] { "TideStaff", "StaffTicks", "Tier0", "Tier1", "Tier2" })
                        r[name].localPosition += new Vector3(lateral, 0, wallDistance);
                    r.Part("GaugeAccess", new MeshKit().Slab(-.65f, .65f, 0, wallDistance, -.1f, 0), mats.Timber, Vector3.zero);
                    r.Part("SurveyMast", new MeshKit().Cylinder(Vector3.zero, .08f, 4.2f, 8), mats.Steel, Vector3.zero);
                    break;
                case DeviceKind.Light: r = LampBuilder.Build(local, parent, mats); break;
                case DeviceKind.Charger:
                    r = ChargerBuilder.Build(local, parent, mats);
                    r.Box("CarPark", new Vector3(0, -.035f, 3), new Vector3(5, .05f, 9), mats.Asphalt);
                    r.Box("ChargeCanopy", new Vector3(0, 3.1f, 1.5f), new Vector3(3.8f, .15f, 4), mats.Steel);
                    break;
                case DeviceKind.Truck: r = TruckBuilder.Build(local, parent, mats); break;
                case DeviceKind.Meter:
                    r = new AssetRig(local, parent);
                    BuildingBuilder.Build(r.Root, Vector3.zero, d.Index == 1, mats, wallDistance);
                    r["Beacon"].localPosition = Vector3.up * 20; r["CardAnchor"].localPosition = Vector3.up * 18;
                    break;
                default: throw new InvalidOperationException("no builder for " + d.Kind);
            }
            r.Placement = actual; rigs.Add(r); Mark("device:" + d.Token, r.Root);
        }
        void Town()
        {
            var random = new System.Random(4242);
            for (float z = -110; z < 250; z += 24)
                for (float x = -410; x < 410; x += 24)
                {
                    // All three land masses are filled; keep the water, quays, buildings,
                    // depot, mapped truck road and mapped flood plain unobstructed.
                    if (x > -245 && x < 240 && z < 140) continue;
                    if (map.FenceRings.Values.Any(r => PresentationMap.Contains(r, new Ground(x, z)))) continue;
                    if (rigs.Any(r => Vector3.Distance(V(r.Placement.At), new Vector3(x, 0, z)) < 28)) continue;
                    if (Vector3.Distance(V(map.Depot), new Vector3(x, 0, z)) < 50) continue;
                    if (RoadDistance(new Ground(x, z)) < 13) continue;
                    var height = 6 + random.Next(4) * 3.2f;
                    BuildingBuilder.House(Root, new Vector3(x, Deck, z), 16 + random.Next(5), 14, height, (int)(height / 3.2f), mats,
                        random.Next(2) == 0 ? mats.Brick : mats.Render, x > 140 ? mats.EastWindows : mats.Windows, true);
                }
        }
        double RoadDistance(Ground p)
        {
            var best = double.MaxValue;
            for (var i = 1; i < map.Road.Count; i++)
            {
                var a = map.Road[i-1]; var b = map.Road[i]; var dx = b.X-a.X; var dz = b.Z-a.Z;
                var t = Math.Max(0, Math.Min(1, ((p.X-a.X)*dx+(p.Z-a.Z)*dz)/Math.Max(.001,dx*dx+dz*dz)));
                best = Math.Min(best, Math.Sqrt(Math.Pow(p.X-a.X-t*dx,2)+Math.Pow(p.Z-a.Z-t*dz,2)));
            }
            return best;
        }
        void Ridge()
        {
            var t = AssetRig.Node("Backdrop ridge", Root, Vector3.zero);
            for (var i = 0; i < 6; i++)
            {
                var y = 22 + i * 28;
                new MeshKit().Slab(-1000, 1000, 265 + i * 48, 1000, 0, y).Spawn("Terrace", t, mats.Grass);
                for (var x = -420; x < 440; x += 36)
                    BuildingBuilder.House(t, new Vector3(x, y, 275 + i * 48), 27, 20, 12, 3, mats, mats.Render, mats.Windows, true);
            }
            new MeshKit().Box(new Vector3(-75, 55, 280), new Vector3(16, 60, 22))
                .Cylinder(new Vector3(-75, 85, 280), 9, 38, 4, 0).Spawn("Church spire", t, mats.Slate);
            new MeshKit().Box(new Vector3(85, 64, 270), new Vector3(12, 85, 12))
                .Gable(new Vector3(85, 107, 270), 16, 16, 10).Spawn("Harbour master clock tower", t, mats.Stone);
            new MeshKit().Pipe(new Vector3(85, 93, 263.9f), Vector3.back, 3.4f, .1f, 32).Spawn("Clock face", t, mats.GaugeBoard);
            new MeshKit().Rod(new Vector3(85, 93, 263.7f), new Vector3(85, 95.5f, 263.7f), .12f)
                .Rod(new Vector3(85, 93, 263.7f), new Vector3(87, 93, 263.7f), .12f).Spawn("Clock hands", t, mats.DarkSteel);
            Mark("ridge", t);
        }
        bool IsPlain(Ground p) => map.FenceRings.Any(r => r.Key.IndexOf("depot", StringComparison.OrdinalIgnoreCase) < 0 && PresentationMap.Contains(r.Value, p));
        void FloodPlain()
        {
            foreach (var ring in map.FenceRings)
            {
                var t = AssetRig.Node("Mapped fence " + ring.Key, Root, Vector3.zero);
                var r = ring.Value;
                var minX=(float)r.Min(p=>p.X); var maxX=(float)r.Max(p=>p.X); var minZ=(float)r.Min(p=>p.Z); var maxZ=(float)r.Max(p=>p.Z);
                // The ring is the mapped polygon itself, never a replacement rectangle.
                for(var i=0;i<r.Length;i++)
                {
                    var ground = ring.Key.IndexOf("depot", StringComparison.OrdinalIgnoreCase) < 0 ? Deck - .7f : Deck;
                    var a=V(r[i],ground+.08f); var b=V(r[(i+1)%r.Length],ground+.08f);
                    new MeshKit().Rod(a,b,.16f).Spawn("Fence ground decal",t,mats.WorldCue,false);
                }
                Slab("Low flood plain",minX,maxX,minZ,maxZ,Deck-.69f,mats.Asphalt,t);
                for(var x=minX+3;x<maxX-2;x+=7)
                    new MeshKit().Box(new Vector3(x,Deck+.1f,(minZ+maxZ)/2),new Vector3(1.8f,1.4f,3.8f)).Spawn("Parked car",t,mats.Render);
            }
            for(var i=1;i<map.Road.Count;i++)
            {
                var a=V(map.Road[i-1],Deck+.055f);var b=V(map.Road[i],Deck+.055f);
                var d=b-a; var mid=(a+b)/2;
                var yaw=Mathf.Atan2(d.x,d.z)*Mathf.Rad2Deg;
                new MeshKit().Box(new Vector3(mid.x,(Deck+SeabedY)/2,mid.z),new Vector3(7,Deck-SeabedY,d.magnitude),yaw).Spawn("Mapped road foundation",Root,mats.Stone);
                new MeshKit().Box(mid,new Vector3(5,.06f,d.magnitude),yaw).Spawn("Mapped truck road",Root,mats.Asphalt);
            }
        }
        void Breakwaters()
        {
            foreach(var pair in new[]{ (map.WestBreakwater, true), (map.EastBreakwater, false) })
            {
                var p=pair.Item1; var x0=(float)p.Min(v=>v.X);var x1=(float)p.Max(v=>v.X);var z0=(float)p.Min(v=>v.Z);var z1=(float)p.Max(v=>v.Z);
                Slab("Breakwater",x0,x1,z0,z1,3,mats.Stone);
                var at=new Vector3(pair.Item2 ? x1-4 : x0+4,3,(z0+z1)/2);
                new MeshKit().Cylinder(at,2,12,12,1.2f).Spawn("Light tower",Root,mats.Render);
                new MeshKit().Cylinder(at+Vector3.up*12,1.5f,1.8f,12).Spawn("Navigation light",Root,pair.Item2?mats.BuoyGreen:mats.BuoyRed);
            }
        }
        void Floats()
        {
            var channelX=(float)map.Mouth.Average(p=>p.X);
            for(var i=0;i<5;i++)
                foreach(var side in new[]{-1,1})
                {
                    var x=channelX+side*(18+i*4);var z=-180+i*38;
                    var t=new MeshKit().Cylinder(new Vector3(0,-.8f,0),.8f,2.6f,10,side<0?.8f:.05f).Spawn("Channel buoy",Root,side<0?mats.BuoyRed:mats.BuoyGreen).transform;
                    buoyRest.Add(new Vector2(x,z));buoys.Add(t);
                }
            for(var i=0;i<5;i++)
            {
                var t=new MeshKit().Box(Vector3.up*.5f,new Vector3(3,1,8)).Box(Vector3.up*1.7f,new Vector3(2,1.3f,3)).Spawn("Moored fishing boat",Root,mats.Render).transform;
                buoyRest.Add(new Vector2(-105+i*43,20+(i%2)*25));buoys.Add(t);
            }
        }
        public void Dispose()
        {
            if(Root==null)return;
            foreach(var f in Root.GetComponentsInChildren<MeshFilter>(true)) MeshKit.Release(f.sharedMesh);
            MeshKit.Release(Root.gameObject);Root=null;
        }
    }
}

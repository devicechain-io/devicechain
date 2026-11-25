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
        WorldPropPlan props;
        readonly List<AssetRig> rigs = new List<AssetRig>();
        readonly List<Transform> buoys = new List<Transform>();
        readonly List<Vector2> buoyRest = new List<Vector2>();
        public Transform Root { get; private set; }
        public IReadOnlyList<AssetRig> Rigs => rigs;
        public IReadOnlyList<Transform> Buoys => buoys;
        public IReadOnlyList<Vector2> BuoyRest => buoyRest;
        public TownBuilder Town { get; private set; }
        /// <summary>The level-of-detail switch for the town's buildings and vehicles (see <see cref="DetailManager"/>).</summary>
        public DetailManager Detail { get; } = new DetailManager();
        public Dictionary<string, List<Renderer>> Masks { get; } = new Dictionary<string, List<Renderer>>();
        public Dictionary<string, QuayFrame> Frames { get; } = new Dictionary<string, QuayFrame>();
        public AssetRig Truck => rigs.First(r => r.Placement.Device.Kind == DeviceKind.Truck);
        static Vector3 V(Ground p, float y = 0) => new Vector3((float)p.X, y, (float)p.Z);
        const float Deck = (float)SitePlan.DeckHeightM;
        HarborWorld(PresentationMap map, HarborMaterials mats) { this.map = map; this.mats = mats; }
        public static HarborWorld Build(PresentationMap map, HarborLayout layout, HarborMaterials mats, Transform parent)
        {
            WorldTags.Clear(); // the tags of a world built before this one went with it
            var w = new HarborWorld(map, mats);
            w.props = WorldPropPlan.Generate(map, layout);
            w.Root = AssetRig.Node("Waterfront", parent, Vector3.zero);
            w.Ground(layout);
            QuayBuilder.Build(map, w.Root, mats, w.props);
            foreach (var token in map.Quays.Keys) w.Mark(token, w.Root.Find(token));
            foreach (var d in layout.Devices) w.Device(d, layout);
            // what is laid on the quays round the devices that hold their own session: part of the quay they are on
            foreach (var token in map.Quays.Keys)
            {
                var fittings = QuayFittingsBuilder.Build(w.Root, mats, w.props.Fittings, token);
                if (fittings != null) w.Mark(token, fittings);
            }
            // The mapped depot sits just south of the eastern breakwater. Give its yard
            // a real foundation and connect it by the mapped road causeway, never a floating slab.
            w.Slab("Depot headland", (float)map.Depot.X - 36, (float)map.Depot.X + 36,
                (float)map.Depot.Z - 26, (float)map.Depot.Z + 26, Deck, mats.Ground);
            if(w.props.Find("depot")!=null)BuildingBuilder.Depot(w.Root, V(map.Depot, Deck), mats, w.Detail);
            w.TownAndHills(); w.FloodPlain(); w.Breakwaters(); w.Floats();
            w.PrebuildDetail();
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
            // With up=(0,.9726,.2326), the top ray leaves the camera 1.55 degrees above the horizontal: the
            // forested hills (TownBuilder.Height) fill the frame's top behind every quay rig.
            // Portrait forward=(0,-.3795,.9252), focal=1920/(2*tan26)=1968 px.
            // At Basin z=90 depth=449 m its horizontal half-width is only 123 m: the
            // prescribed 52 degree portrait pose cannot contain x=-155..-230 (see README).
            // The core round the harbour is the deck; outside it TownBuilder's terrain rises from the same height.
            Slab("North hinterland", -TownBuilder.CoreHalfX, TownBuilder.CoreHalfX, (float)north.MaxZ, TownBuilder.CoreNorthZ, Deck, mats.Ground);
            Slab("North west connection", (float)basin.MinX, (float)north.MinX, (float)basin.MaxZ, (float)north.MaxZ, Deck, mats.Ground);
            Slab("North east connection", (float)north.MaxX, (float)east.MaxX, (float)east.MaxZ, (float)north.MaxZ, Deck, mats.Ground);
            Slab("West shoulder", (float)basin.MaxX, (float)north.MinX, (float)inner.MaxZ, (float)basin.MaxZ, Deck, mats.Ground);
            Slab("West hinterland", -TownBuilder.CoreHalfX, (float)basin.MinX, (float)inner.MinZ, (float)north.MaxZ, Deck, mats.Ground);
            Slab("East hinterland", (float)east.MaxX, TownBuilder.CoreHalfX, (float)inner.MinZ, (float)north.MaxZ, Deck, mats.Ground);
            Slab("Harbour floor", -9000, 9000, -9000, 3000, SeabedY, mats.Seabed);
            foreach (var token in map.Quays.Keys)
            {
                var f = new QuayFrame(map, token); Frames.Add(token, f);
                var t = f.Parent(Root, "ground:" + token);
                // Tile the quay top round each well's actual transformed disc. No opaque deck
                // may close a U1 cut-away. A small square aperture surrounds each round shell.
                var pumps = layout.Devices.Where(d => map.QuayFor(d) == token && d.Kind == DeviceKind.Pump)
                    .Select(d => f.Local(V(map.Anchors[d.Token])) + new Vector3((float)WellFootprint.LocalX, 0, (float)WellFootprint.LocalZ)).ToArray();
                var paving = new MeshKit();
                // Join adjacent top tiles with the same height. Keep exactly the original
                // aperture/low-plain decisions, saving vertices to pay for the exposed skirts.
                for (float z = -f.Depth; z < 0; z += 2)
                {
                    float start = 0; bool? stripLow = null;
                    for (float x = -f.Length / 2; x < f.Length / 2 + 2; x += 2)
                    {
                        var clear = x >= f.Length / 2 || pumps.Any(p => Math.Abs(p.x - (x + 1)) < 3.5 && Math.Abs(p.z - (z + 1)) < 3.5);
                        var center = f.World(new Vector3(x + 1, 0, z + 1));
                        var low = IsPlain(new Ground(center.x, center.z));
                        if (stripLow.HasValue && (clear || stripLow.Value != low)) {
                            var y = stripLow.Value ? Deck - .7f : Deck; var end = Math.Min(x, f.Length / 2);
                            paving.Quad(new Vector3(start,y,z),new Vector3(start,y,Math.Min(z+2,0)),
                                new Vector3(end,y,Math.Min(z+2,0)),new Vector3(end,y,z));
                            stripLow = null;
                        }
                        if (!clear && !stripLow.HasValue) { start = x; stripLow = low; }
                    }
                }
                var pavement=paving.Spawn("Quay paving", t, mats.Surfaces);
                SurfaceDetail.Colour(pavement,new Color(.59f,.57f,.54f,.1f));
                Slab("Wall", -f.Length / 2, f.Length / 2, -.8f, 0, Deck + .35f, mats.Stone, t);
                // The front seawall closes local +Z. Close both ends and the landward edge
                // down to the seabed too, including East/Basin's exposed southern ends.
                var skirt = new MeshKit(); var left = -f.Length / 2; var right = f.Length / 2;
                skirt.Quad(new Vector3(left,SeabedY,0),new Vector3(left,Deck,0),new Vector3(left,Deck,-f.Depth),new Vector3(left,SeabedY,-f.Depth));
                skirt.Quad(new Vector3(right,SeabedY,-f.Depth),new Vector3(right,Deck,-f.Depth),new Vector3(right,Deck,0),new Vector3(right,SeabedY,0));
                skirt.Quad(new Vector3(left,SeabedY,-f.Depth),new Vector3(left,Deck,-f.Depth),new Vector3(right,Deck,-f.Depth),new Vector3(right,SeabedY,-f.Depth));
                skirt.Spawn("Quay perimeter", t, mats.Stone);
                var promenade=Slab("Promenade", -f.Length / 2, f.Length / 2, -7, -2, Deck + .015f, mats.Surfaces, t);
                SurfaceDetail.Colour(promenade.gameObject,new Color(.56f,.54f,.50f,.2f));
                // Asphalt is a finish on the existing deck, not a seabed-to-deck dark slab at the outer corners.
                var roadMesh=new MeshKit();
                roadMesh.Quad(new Vector3(-f.Length/2,Deck+.03f,-f.Depth+2),new Vector3(-f.Length/2,Deck+.03f,-f.Depth+9),
                    new Vector3(f.Length/2,Deck+.03f,-f.Depth+9),new Vector3(f.Length/2,Deck+.03f,-f.Depth+2));
                var road=roadMesh.Spawn("Quay road",t,mats.Surfaces);
                SurfaceDetail.Colour(road,new Color(.31f,.31f,.29f,.3f));
                SurfaceDetail.Quay(t,mats,f);
                AssetRig.MergeFixed(t,t.GetComponentsInChildren<MeshFilter>());
                Mark(token, t);
            }
        }
        void Device(LayoutDevice d, HarborLayout layout)
        {
            var quay = map.QuayFor(d);
            var f = d.Kind == DeviceKind.Truck ? null : Frames[quay];
            var at = map.Anchors[d.Token];
            var actual = new Placement(d, at, d.Kind == DeviceKind.Truck ? 0 : quay == "hp-north-quay" ? 180 : quay == "hp-east-quay" ? 270 : 90);
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
                    var lateral = quay == "hp-north-quay" ? 0 : d.Kind == DeviceKind.LoraGauge ? -10 : d.Kind == DeviceKind.LegacyGauge ? -7 : (d.Index - 2) * 3;
                    foreach (var name in new[] { "TideStaff", "StaffTicks", "Tier0", "Tier1", "Tier2" })
                        r[name].localPosition += new Vector3(lateral, 0, wallDistance);
                    r.Part("GaugeAccess", new MeshKit().Slab(-.65f, .65f, 0, wallDistance, -.1f, 0), mats.Timber, Vector3.zero);
                    r.Part("SurveyMast", new MeshKit().Cylinder(Vector3.zero, .08f, 4.2f, 8), mats.Steel, Vector3.zero);
                    break;
                case DeviceKind.Light: r = LampBuilder.Build(local, parent, mats); break;
                case DeviceKind.Charger:
                    var car=props.Find("car:"+d.Token);
                    var localCar=f.Local(V(car.At))-f.Local(V(at));
                    r = ChargerBuilder.Build(local, parent, mats, localCar, Detail);
                    r.Box("CarPark", localCar+new Vector3(0, -.035f, -1), new Vector3(5, .05f, 9), mats.Asphalt);
                    r.Box("ChargeCanopy", localCar+new Vector3(0, 3.1f, -2.5f), new Vector3(3.8f, .15f, 4), mats.Steel);
                    break;
                case DeviceKind.MqttCharger: case DeviceKind.CertCharger:
                    var bay=props.Find("car:"+d.Token);
                    var localBay=f.Local(V(bay.At))-f.Local(V(at));
                    r = LinkedChargerBuilder.Build(local, parent, mats, localBay, d.Kind==DeviceKind.CertCharger, Detail);
                    // the bay's asphalt, laid on the court's setts between its painted lines
                    r.Box("CarPark", localBay+new Vector3(0, .014f, -.9f), new Vector3(2.7f, .02f, 5.8f), mats.Asphalt);
                    break;
                case DeviceKind.PullLight: r = PullLampBuilder.Build(local, parent, mats); break;
                case DeviceKind.Truck: r = TruckBuilder.Build(local, parent, mats, Detail); break;
                case DeviceKind.Meter:
                    r = new AssetRig(local, parent);
                    BuildingBuilder.Build(r.Root, Vector3.zero, d.Index == 1, mats, wallDistance, Detail);
                    r["Beacon"].localPosition = Vector3.up * 20; r["CardAnchor"].localPosition = Vector3.up * 18;
                    break;
                default: throw new InvalidOperationException("no builder for " + d.Kind);
            }
            r.Placement = actual; rigs.Add(r); Mark("device:" + d.Token, r.Root);
        }
        void TownAndHills()
        {
            Town = TownBuilder.Build(Root, mats, props, Detail);
            Mark("ridge", Town.Backdrop);
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
                // parked cars on the low plain (its surface is 0.69 m below the deck): a body and a cabin, in a few colours
                var paints=new[]{mats.HullWhite,mats.ClapBlue,mats.BarnRed,mats.Slate,mats.ClapGrey};var n=0;
                // the far bodies are kept out of the fence's merged batch so a near car can replace its own (a complete model, see NearVehicles.Car)
                var carNode=AssetRig.Node("Parked cars "+ring.Key,Root,Vector3.zero);
                foreach(var car in props.Props.Where(p=>p.Kind=="ParkedCar" && p.Area==ring.Key))
                {
                    var x=(float)car.At.X;var z=(float)car.At.Z;var floor=ring.Key.IndexOf("depot",StringComparison.OrdinalIgnoreCase)<0?Deck-.69f:Deck;
                    var paint=paints[n++%paints.Length];
                    var body=new MeshKit().Box(new Vector3(x,floor+.55f,z),new Vector3((float)car.Width,.8f,(float)car.Depth)).Box(new Vector3(x,floor+1.22f,z+.2f),new Vector3(1.6f,.6f,2.3f)).Spawn("Parked car",carNode,paint);
                    var glass=new MeshKit().Box(new Vector3(x,floor+1.22f,z+.2f),new Vector3(1.64f,.42f,2.1f)).Spawn("Car glass",carNode,mats.Glass,false);
                    Detail.Register(NearVehicles.Car(CarSpec.Parked),carNode,new Vector3(x,floor,z),Quaternion.identity,NearPalette.Vehicle(paint.GetColor("_BaseColor")),mats.Surfaces,null,false,
                        new[]{body.GetComponent<MeshRenderer>(),glass.GetComponent<MeshRenderer>()});
                }
                SurfaceDetail.Parking(t,mats,minX,maxX,minZ,maxZ,ring.Key.IndexOf("depot",StringComparison.OrdinalIgnoreCase)<0?Deck-.64f:Deck+.11f);
                AssetRig.MergeFixed(t,t.GetComponentsInChildren<MeshFilter>());

            }
            for(var i=1;i<map.Road.Count;i++)
            {
                var a=V(map.Road[i-1],Deck+.055f);var b=V(map.Road[i],Deck+.055f);
                var d=b-a; var mid=(a+b)/2;
                var yaw=Mathf.Atan2(d.x,d.z)*Mathf.Rad2Deg;
                new MeshKit().Box(new Vector3(mid.x,(Deck+SeabedY)/2,mid.z),new Vector3(7,Deck-SeabedY,d.magnitude),yaw).Spawn("Mapped road foundation",Root,mats.Stone);
                var road=new MeshKit().Box(mid,new Vector3(5,.06f,d.magnitude),yaw).Spawn("Mapped truck road",Root,mats.Surfaces);
                SurfaceDetail.Colour(road,new Color(.18f,.18f,.17f,.3f));
                SurfaceDetail.Road(Root,mats,a,b);
            }
        }
        void Breakwaters()
        {
            foreach(var pair in new[]{ (map.WestBreakwater, true), (map.EastBreakwater, false) })
            {
                var p=pair.Item1; var x0=(float)p.Min(v=>v.X);var x1=(float)p.Max(v=>v.X);var z0=(float)p.Min(v=>v.Z);var z1=(float)p.Max(v=>v.Z);
                Slab("Breakwater",x0,x1,z0,z1,3,mats.Stone);
                var at=new Vector3(pair.Item2 ? x1-4 : x0+4,3,(z0+z1)/2);
                NavigationBuilder.Beacon(at,Root,mats,pair.Item2);
            }
        }
        void Floats()
        {
            var channelX=(float)map.Mouth.Average(p=>p.X);
            for(var i=0;i<5;i++)
                foreach(var side in new[]{-1,1})
                {
                    var x=channelX+side*(18+i*4);var z=-180+i*38;
                    var t=NavigationBuilder.Buoy(Root,mats,side<0);
                    buoyRest.Add(new Vector2(x,z));buoys.Add(t);
                }
            // the town's moored boats ride the same surface as the buoys
            PierDetailBuilder.Build(Root,mats);
            for(var i=0;i<Town.Boats.Count;i++)
            {
                BoatBuilder.Refine(Town.Boats[i],mats,i,Detail);
                buoys.Add(Town.Boats[i]);buoyRest.Add(Town.BoatRest[i]);
            }
        }
        /// <summary>
        /// Builds, inactive, the near sets any story pose could bring within reach, so the show's cuts do not hitch on them. The poses come from the
        /// presentation map (the fixed ones, the per-device ones, and every pose the storyboard names); one the map does not know is skipped.
        /// </summary>
        void PrebuildDetail()
        {
            var names = new List<string>(DetailCandidates.PoseNames());
            var story = Resources.Load<TextAsset>("storyboard");
            if (story != null) foreach (var n in DetailCandidates.PoseNamesIn(story.text)) if (!names.Contains(n)) names.Add(n);
            var cameras = new List<NearVec>(); var looks = new List<NearVec>();
            foreach (var name in names)
            {
                CameraPose pose;
                try { pose = map.Pose(name); } catch (FormatException) { continue; }
                cameras.Add(new NearVec((float)pose.Camera.X, (float)pose.Camera.Y, (float)pose.Camera.Z));
                looks.Add(new NearVec((float)pose.Look.X, (float)pose.Look.Y, (float)pose.Look.Z));
            }
            Detail.Prebuild(cameras, looks);
        }
        public void Dispose()
        {
            if(Root==null)return;
            Town?.Dispose();
            foreach(var f in Root.GetComponentsInChildren<MeshFilter>(true)) MeshKit.Release(f.sharedMesh);
            MeshKit.Release(Root.gameObject);Root=null;
        }
    }
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using DeviceChain.Harborpulse.Model.Director;

namespace DeviceChain.Harborpulse.Model
{
    public sealed class SceneryStreet
    {
        public string Name { get; }
        public double Width { get; }
        public Ground[] Points { get; }
        public double Length { get; }
        public SceneryStreet(string name, double width, params Ground[] points)
        {
            Name=name; Width=width; Points=points;
            for(var i=1;i<points.Length;i++) Length+=Distance(points[i-1],points[i]);
        }
        public static double Distance(Ground a,Ground b) => Math.Sqrt((a.X-b.X)*(a.X-b.X)+(a.Z-b.Z)*(a.Z-b.Z));
        public void At(double s,out Ground p,out Ground t)
        {
            for(var i=1;i<Points.Length;i++)
            {
                var a=Points[i-1];var b=Points[i];var length=Distance(a,b);
                if(s<=length || i==Points.Length-1)
                { t=new Ground((b.X-a.X)/length,(b.Z-a.Z)/length);p=new Ground(a.X+t.X*Math.Min(s,length),a.Z+t.Z*Math.Min(s,length));return; }
                s-=length;
            }
            p=Points[0];t=new Ground(0,1);
        }
        public double DistanceTo(Ground p)
        {
            var best=double.MaxValue;
            for(var i=1;i<Points.Length;i++) best=Math.Min(best,SegmentDistance(p,Points[i-1],Points[i]));
            return best;
        }
        public static double SegmentDistance(Ground p,Ground a,Ground b)
        {
            var dx=b.X-a.X;var dz=b.Z-a.Z;
            var u=Math.Max(0,Math.Min(1,((p.X-a.X)*dx+(p.Z-a.Z)*dz)/Math.Max(.0001,dx*dx+dz*dz)));
            return Distance(p,new Ground(a.X+u*dx,a.Z+u*dz));
        }
    }

    public sealed class WorldProp
    {
        public string Kind { get; }
        public PropFootprint Footprint { get; }
        public Ground At { get; }
        public double Yaw { get; }
        public double Width { get; }
        public double Depth { get; }
        public double Height { get; }
        public string Area { get; }
        /// <summary>A tree's <see cref="TreeKind"/>, or a rock's index in <see cref="WorldPropPlan.Outcrops"/>; 0 for everything else.</summary>
        public int Variant { get; }
        public WorldProp(string kind, PropFootprint footprint, Ground at, double yaw, double width, double depth, double height=0,string area="",int variant=0)
        { Kind=kind;Footprint=footprint;At=at;Yaw=yaw;Width=width;Depth=depth;Height=height;Area=area;Variant=variant; }
    }

    /// <summary>Complete deterministic ground-prop plan. Builders draw these accepted placements, never independently recompute candidates.
    /// Structural deck, rails, buried plumbing and overhead canopy are attached surfaces, not independent ground props.
    /// Tree footprints include the entire crown and lean; building footprints include roofs, porches and rear sheds.</summary>
    public sealed class WorldPropPlan
    {
        public const double CarWidth=2.2, CarBodyWidth=1.8, CarDepth=3.6, ParkedWidth=1.8, ParkedDepth=4.3;
        public FootprintRegistry Registry { get; } = new FootprintRegistry();
        readonly List<WorldProp> props=new List<WorldProp>();
        readonly List<SceneryStreet> streets=new List<SceneryStreet>();
        public IReadOnlyList<WorldProp> Props => props.AsReadOnly();
        public IReadOnlyList<SceneryStreet> Streets => streets.AsReadOnly();
        /// <summary>The terrain mesh's grid: rocks and trees stand on the surface it draws, and the visual builder draws exactly this grid.</summary>
        public TerrainGrid Terrain { get; } = TerrainGrid.Standard();
        readonly List<Outcrop> outcrops=new List<Outcrop>();
        public IReadOnlyList<Outcrop> Outcrops => outcrops.AsReadOnly();
        public WorldProp Find(string id) => props.FirstOrDefault(p=>p.Footprint.Id==id);
        readonly PresentationMap map;
        readonly Random random=new Random(1789);
        int serial;
        WorldPropPlan(PresentationMap map) { this.map=map; }
        public static WorldPropPlan Generate(PresentationMap map,HarborLayout layout)
        {
            var p=new WorldPropPlan(map);
            // The new rock and tree generation draws from its own seeded streams and runs after everything that fixes the town, so the
            // streets, lots, houses and lamps are exactly what they were; lamps now go first so no tree can displace one.
            p.AddStreets(); p.Devices(layout); p.Waterfront(); p.Lots(); p.Lamps(); p.Rocks(); p.Trees();
            return p;
        }
        bool Add(string kind,Ground at,double w,double d,double yaw=0,double height=0,string area="",string id=null,double apron=0,int variant=0)
        {
            var footprint=new PropFootprint(id??kind+":"+serial++,at,w+apron,d+apron,yaw);
            if(!Registry.TryAdd(footprint)) return false;
            props.Add(new WorldProp(kind,footprint,at,yaw,w,d,height,area,variant));return true;
        }
        public static double QuayYaw(string token) => token=="hp-north-quay" ? 180 : token=="hp-east-quay" ? 270 : 90;
        public static PropFootprint QuayFrame(PresentationMap map,string token)
        {
            var q=map.Quays[token];var x0=q.Min(p=>p.X);var x1=q.Max(p=>p.X);var z0=q.Min(p=>p.Z);var z1=q.Max(p=>p.Z);
            var north=token=="hp-north-quay";var east=token=="hp-east-quay";
            return new PropFootprint(token,north?new Ground((x0+x1)/2,z0):new Ground(east?x0:x1,(z0+z1)/2),north?x1-x0:z1-z0,north?z1-z0:x1-x0,QuayYaw(token));
        }
        /// <summary>
        /// The strip a wet well is looked into across: from the well's water-side edge out past where the well view's eye stands
        /// (<see cref="PresentationMap.WellViewBackM"/>), and <see cref="WellViewHalfWidthM"/> either side of that line. Nothing with a roof is placed in it.
        /// </summary>
        public const double WellViewHalfWidthM=5.2;
        public bool InWellView(Ground p)
        {
            foreach(var pump in layoutDevices)
            {
                if(pump.Kind!=DeviceKind.Pump)continue;
                var frame=new PropFootprint(pump.Token,map.Anchors[pump.Token],1,1,QuayYaw(map.QuayFor(pump)));
                // into the pump's own frame: the well is at (LocalX, LocalZ) and its open front faces local +z
                var a=frame.Yaw*Math.PI/180;double dx=p.X-frame.At.X,dz=p.Z-frame.At.Z;
                double lx=dx*Math.Cos(a)-dz*Math.Sin(a),lz=dx*Math.Sin(a)+dz*Math.Cos(a);
                if(Math.Abs(lx-WellFootprint.LocalX)<=WellViewHalfWidthM&&lz>=WellFootprint.LocalZ&&lz<=WellFootprint.LocalZ+PresentationMap.WellViewBackM+2)return true;
            }
            return false;
        }
        LayoutDevice[] layoutDevices=new LayoutDevice[0];
        /// <summary>The cabinets, ducts and paving round the chargers that hold their own session and the pull lamps (see <see cref="QuayFittings"/>).</summary>
        public QuayFittings Fittings { get; private set; }
        void Devices(HarborLayout layout)
        {
            layoutDevices=layout.Devices.ToArray();
            foreach(var device in layout.Devices)
            {
                var at=map.Anchors[device.Token];var yaw=device.Kind==DeviceKind.Truck?0:QuayYaw(map.QuayFor(device));
                var frame=new PropFootprint(device.Token,at,1,1,yaw);
                void Fixed(string suffix,double x,double z,double w,double d) => Registry.AddFixed(new PropFootprint("device:"+device.Token+suffix,frame.Local(x,z),w,d,yaw));
                switch(device.Kind)
                {
                    case DeviceKind.Meter: Fixed("",device.Index==1?0:NearLandmarks.TerminalOffsetX,0,device.Index==1?39.2:NearLandmarks.TerminalW+1,device.Index==1?19.2:23);break;
                    case DeviceKind.Pump:
                        Fixed("",-.7,-.7,11.8,8.6); Fixed(":well",WellFootprint.LocalX,WellFootprint.LocalZ,5.6,5.6);break;
                    case DeviceKind.Truck: Fixed("",0,0,3.4,7.6);break;
                    case DeviceKind.Light: Fixed("",0,.8,.8,2.8);break;
                    case DeviceKind.Charger: case DeviceKind.MqttCharger: case DeviceKind.CertCharger: Fixed("",0,.55,1.9,2.1);break;
                    case DeviceKind.PullLight: Fixed("",0,0,1.1,1.1);break;
                    default:
                        Fixed("",0,0,1.5,1.5);
                        var token=map.QuayFor(device);var wall=QuayFrame(map,token);
                        var localZ=(at.X-wall.At.X)*Math.Sin(yaw*Math.PI/180)+(at.Z-wall.At.Z)*Math.Cos(yaw*Math.PI/180);
                        var lateral=token=="hp-north-quay"?0:device.Kind==DeviceKind.LoraGauge?-10:device.Kind==DeviceKind.LegacyGauge?-7:(device.Index-2)*3;
                        Fixed(":staff",lateral,-localZ+.64,.6,.2);
                        break;
                }
            }
            // what is laid round the devices that hold their own session: a cabinet for each pair of chargers, standing on the ground like any prop
            Fittings=QuayFittings.Plan(map,layout);
            foreach(var court in Fittings.Courts) Registry.AddFixed(court.CabinetFootprint);
            foreach(var device in layout.Devices.Where(d=>DeviceKind.HasBay(d.Kind)))
            {
                var frame=new PropFootprint(device.Token,map.Anchors[device.Token],1,1,QuayYaw(map.QuayFor(device)));
                var placed=false;
                foreach(var z in new[]{4.0,7.0})
                {
                    foreach(var x in new[]{0.0,-3.0,3.0,-6.0,6.0})
                    {
                        // never in front of a wet well's cut-away: the bay's canopy stood between the well view's eye and East Quay's well
                        if(InWellView(frame.Local(x,z)))continue;
                        if(Add("ChargingCar",frame.Local(x,z),CarWidth,CarDepth,frame.Yaw,area:device.Token,id:"car:"+device.Token)) { placed=true; break; }
                    }
                    if(placed)break;
                }
                if(!placed) throw new InvalidOperationException("No clear charging bay for "+device.Token);
            }
            Add("Depot",new Ground(map.Depot.X-18,map.Depot.Z-14),29,17,id:"depot");
            foreach(var ring in map.FenceRings)
            {
                var minX=ring.Value.Min(p=>p.X);var maxX=ring.Value.Max(p=>p.X);var z=(ring.Value.Min(p=>p.Z)+ring.Value.Max(p=>p.Z))/2;
                var n=0;
                for(var x=minX+3;x<maxX-2;x+=7,n++) Add("ParkedCar",new Ground(x,z),ParkedWidth,ParkedDepth,area:ring.Key,id:"park:"+ring.Key+":"+n);
            }
            foreach(var token in map.Quays.Keys)
            {
                var f=QuayFrame(map,token);
                for(var x=-f.Width/2+12;x<f.Width/2-6;x+=28)
                {
                    Add("Bollard",f.Local(x,-.9),3.6,1.3,f.Yaw,area:token,id:"bollard:"+token+":"+x);
                    Add("Cleat",f.Local(x+8,-1),1.5,.6,f.Yaw,area:token);
                }
            }
        }
        void AddStreets()
        {
            streets.Add(new SceneryStreet("Main Street", 10.5, new Ground(-46, 124), new Ground(-44, 158), new Ground(-34, 198), new Ground(-16, 238),
                new Ground(-2, 280), new Ground(-8, 322), new Ground(-30, 366), new Ground(-62, 410)));
            streets.Add(new SceneryStreet("Elm Street", 7, new Ground(-30, 192), new Ground(-78, 202), new Ground(-132, 222), new Ground(-190, 246), new Ground(-262, 262), new Ground(-330, 260)));
            streets.Add(new SceneryStreet("Church Street", 7, new Ground(-12, 250), new Ground(42, 252), new Ground(98, 266), new Ground(150, 286), new Ground(214, 306), new Ground(286, 312)));
            streets.Add(new SceneryStreet("Shore Road", 7, new Ground(108, 124), new Ground(170, 132), new Ground(236, 146), new Ground(292, 160), new Ground(330, 196), new Ground(350, 250)));
            streets.Add(new SceneryStreet("Wharf Street", 7, new Ground(-41, 166), new Ground(18, 170), new Ground(78, 164), new Ground(138, 174), new Ground(204, 186), new Ground(262, 204)));
            streets.Add(new SceneryStreet("School Street", 6, new Ground(-43, 172), new Ground(-98, 168), new Ground(-122, 186), new Ground(-168, 198)));
            streets.Add(new SceneryStreet("Hill Lane", 6, new Ground(-7, 322), new Ground(46, 344), new Ground(102, 352), new Ground(150, 372)));
            streets.Add(new SceneryStreet("West Lane", 6, new Ground(-236, 168), new Ground(-292, 150), new Ground(-340, 110), new Ground(-380, 52), new Ground(-430, 4)));
        }
        double Clearance(Ground p) => streets.Min(s=>s.DistanceTo(p)-s.Width/2);
        bool Reserved(Ground p,double margin=0)
        {
            if(map.FenceRings.Values.Any(r=>PresentationMap.Contains(r,p)))return true;
            for(var i=1;i<map.Road.Count;i++) if(SceneryStreet.SegmentDistance(p,map.Road[i-1],map.Road[i])<13+margin)return true;
            return false;
        }
        bool Free(Ground p,double radius,bool park=false)
        {
            if(Math.Abs(p.X)<240+radius*.5 && p.Z<140+radius*.5 && !park)return false;
            if(Reserved(p,radius))return false;
            if(map.Anchors.Values.Any(a=>SceneryStreet.Distance(a,p)<30+radius))return false;
            if(!park && p.X>-240-radius && p.X<-120+radius && p.Z>72-radius && p.Z<180+radius)return false;
            return true;
        }
        void Waterfront()
        {
            Add("Church",new Ground(30,274),14,38,180,id:"church");
            foreach(var s in new[]{new[]{132.0,106,20,11},new[]{162.0,108,18,10},new[]{198.0,102,22,12}})
                Add("Shed",new Ground(s[0],s[1]),s[2],s[3],s[0]>190?200:180,id:"yard:"+s[0],apron:2);
            Add("Gazebo",new Ground(-140,86),8.4,8.4,id:"gazebo");
            Add("HauledBoat",new Ground(150,86),4,12,70,id:"hauled");
            Add("Crates",new Ground(187.4,86),4.2,1.2,id:"crates");
            foreach(var b in new[]{new Ground(-160,116),new Ground(-208,140)}) Add("Bench",b,2.3,.8,45);
            foreach(var pier in new[]{new[]{140.0,-70,-90,46,4.2},new[]{-155.0,-102,90,42,4.2}})
            {
                var f=new PropFootprint("pier",new Ground(pier[0],pier[1]),1,1,pier[2]);
                for(var z=6.0;z<pier[3];z+=10)foreach(var side in new[]{-1,1})
                    Add("PierBollard",f.Local(side*(pier[4]/2-.35),z),.5,.5,area:pier[0]>0?"east":"west");
            }
        }
        void Lots()
        {
            var house=0;
            foreach(var s in streets)foreach(var side in new[]{-1,1})
            {
                var at=6+random.NextDouble()*6;var inn=false;
                while(at<s.Length-8)
                {
                    string kind;double w,d;
                    if(s.Name=="Main Street" && at<120)kind=random.NextDouble()<.6?"Commercial":"Shopfront";
                    else if(s.Name=="Main Street" && !inn && side<0 && at>130){kind="Inn";inn=true;}
                    else if(s.Name=="Shore Road" && at<140)kind="Shed";else kind="House";
                    switch(kind){case "Commercial":w=15+random.Next(6);d=13;break;case "Shopfront":w=10+random.Next(3);d=11;break;
                        case "Inn":w=24;d=14;break;case "Shed":w=18+random.Next(6);d=11;break;default:w=9+random.Next(4);d=8+random.Next(3);break;}
                    s.At(at+w/2,out var p,out var t);var n=new Ground(t.Z*side,-t.X*side);
                    var setback=kind=="Commercial"||kind=="Shopfront"?1.5:3+random.NextDouble()*4;
                    // Seven metres includes the veranda, rear shed and roof overhang.
                    var offset=s.Width/2+setback+d/2+3.5;
                    var c=new Ground(p.X+n.X*offset,p.Z+n.Z*offset);var yaw=Math.Atan2(-n.X,-n.Z)*180/Math.PI;
                    var radius=.5*Math.Sqrt(w*w+(d+7)*(d+7));
                    var footprint=new PropFootprint("candidate",c,w+1,d+7,yaw);
                    var corners=new[]{footprint.Local(-footprint.Width/2,-footprint.Depth/2),footprint.Local(footprint.Width/2,-footprint.Depth/2),footprint.Local(-footprint.Width/2,footprint.Depth/2),footprint.Local(footprint.Width/2,footprint.Depth/2)};
                    if(Free(c,radius) && corners.All(v=>Clearance(v)>.8) && (kind!="House"||CoastalTerrain.KeepHouse(house++)))
                    {
                        if(Registry.TryAdd(new PropFootprint("lot:"+serial++,c,w+1,d+7,yaw)))
                        {var f=Registry.Entries[Registry.Entries.Count-1];props.Add(new WorldProp(kind,f,c,yaw,w,d,area:"town"));}
                    }
                    at+=w+(kind=="Commercial"||kind=="Shopfront"?1.5+random.NextDouble()*3:3.5+random.NextDouble()*7);
                }
            }
        }
        // ---------------------------------------------------------------- granite outcrops

        double Slope(Ground p)
        {
            var t=Terrain;
            var gx=(t.MeshHeight(p.X+10,p.Z)-t.MeshHeight(p.X-10,p.Z))/20;
            var gz=(t.MeshHeight(p.X,p.Z+10)-t.MeshHeight(p.X,p.Z-10))/20;
            return Math.Sqrt(gx*gx+gz*gz);
        }
        bool LocalSummit(Ground p)
        {
            var h=Terrain.MeshHeight(p.X,p.Z)+.5;
            for(var k=0;k<8;k++)
            {
                var a=k*Math.PI/4;
                if(Terrain.MeshHeight(p.X+Math.Cos(a)*100,p.Z+Math.Sin(a)*100)>h)return false;
            }
            return true;
        }

        /// <summary>
        /// Clusters of two to five low granite ledges, each fitted to its slope and sunk into it: on the shore outside the quays, in the
        /// meadow clearings on knoll flanks and along woodland edges. Own seeded choices; they never touch the town's random stream.
        /// </summary>
        void Rocks()
        {
            var candidates=new List<(Ground At,int Class,double Rank)>();
            for(var z=-125.0;z<1250;z+=60)for(var x=-1400.0;x<1400;x+=60)
            {
                var p=new Ground(x+(CoastalTerrain.Hash((int)x,(int)z+101)-.5)*40,z+(CoastalTerrain.Hash((int)x+77,(int)z)-.5)*40);
                var d=CoastalTerrain.CoreDistance(p.X,p.Z);
                if(d<60||p.Z<-125)continue;
                var cls=-1;
                if(p.Z<-30&&Math.Abs(p.X)>300&&Math.Abs(p.X)<1100)cls=0;
                else if(d>300&&d<1150)
                {
                    var slope=Slope(p);
                    if(slope>=.025&&slope<=.28)
                    {
                        if(CoastalTerrain.ClearingWeight(p.X,p.Z)>.3)cls=1;
                        else{var cover=CoastalTerrain.ForestCover(p.X,p.Z);if(cover>.3&&cover<.7)cls=2;}
                    }
                }
                if(cls<0||LocalSummit(p))continue;
                // prefer clusters that can be seen: the first rank is a hash, pulled toward the shore beside the quays and the land 400-900 m out
                candidates.Add((p,cls,CoastalTerrain.Hash((int)x*3+11,(int)z*5-7)*.5+(cls==0?Math.Abs(Math.Abs(p.X)-450)/1500:Math.Abs(d-550)/1600)));
            }
            candidates.Sort((a,b)=>a.Rank.CompareTo(b.Rank));
            var caps=new[]{4,7,5};var taken=new int[3];var centres=new List<Ground>();var cluster=0;
            // The landmark tor first, so every cluster seats itself round it: at the back of the meadow on the town's east edge, 830 m from
            // the 16:9 HOME's eye and clear of the trees in front of it.
            var m1=CoastalTerrain.Meadow(LandmarkMeadow);
            var landmark=PlaceLandmark("rock:landmark",new Ground(m1.X+m1.R*LandmarkOffsetX,m1.Z+m1.R*LandmarkOffsetZ),7001,Outcrop.LandmarkRiseM,false,0);
            if(landmark!=null){centres.Add(landmark.Center);EastTor=landmark;}
            // and its lesser sister in the wood west of the Basin, 360 m in front of the 9:16 HOME's eye, which looks east along the harbour and
            // never held the first: its long axis lies north-south, across that eye's line of sight
            var west=PlaceLandmark("rock:landmark-west",new Ground(WestTorX,WestTorZ),7101,Outcrop.WestTorRiseM,true,90);
            if(west!=null)
            {
                centres.Add(west.Center);WestTor=west;
                // with a few low ledges at its foot, on the side the harbour is: a tor is the highest of an outcrop, not a stack alone in a wood
                PlaceCluster(new Ground(west.Center.X+WestTorLedgesDX,west.Center.Z+WestTorLedgesDZ),0,WestTorLedgesSeed);
            }
            // The meadows HOME looks at each get a cluster of large ledges inside or at the edge of the clearing, so the rock reads from there.
            for(var i=0;i<CoastalTerrain.VisibleMeadows;i++)
            {
                var m=CoastalTerrain.Meadow(i);var angle=CoastalTerrain.Hash(i*7+3,11)*2*Math.PI;
                var c=new Ground(m.X+Math.Cos(angle)*m.R*.45,m.Z+Math.Sin(angle)*m.R*.45);
                if(PlaceCluster(c,3,cluster)>=2){centres.Add(c);cluster++;}
            }
            foreach(var c in candidates)
            {
                if(taken[c.Class]>=caps[c.Class]||centres.Any(o=>SceneryStreet.Distance(o,c.At)<190))continue;
                if(PlaceCluster(c.At,c.Class,cluster)>=2){taken[c.Class]++;centres.Add(c.At);cluster++;}
            }
        }

        /// <summary>The meadow the landmark tor stands in (<see cref="CoastalTerrain.Meadow"/>) and where in it, as a share of its radius from its centre.</summary>
        public const int LandmarkMeadow=1; public const double LandmarkOffsetX=-.20,LandmarkOffsetZ=.42;

        /// <summary>Where the west tor stands (<see cref="Outcrop.WestTorRiseM"/>): in the wood between the Basin and the western streets.</summary>
        public const double WestTorX=-345,WestTorZ=-15;
        /// <summary>The cluster of low ledges at the west tor's foot: its centre from the tor's, and its seed.</summary>
        public const double WestTorLedgesDX=34,WestTorLedgesDZ=-26;public const int WestTorLedgesSeed=93;

        /// <summary>The landmark tor on the town's east edge, which the 16:9 HOME looks at, and the lesser one west of the Basin, which the 9:16 HOME does.</summary>
        public Outcrop EastTor{get;private set;}
        public Outcrop WestTor{get;private set;}

        /// <summary>A landmark tor (<see cref="Outcrop.Landmark"/>): seated at the first of a few spots round its place that can take its ledge.</summary>
        Outcrop PlaceLandmark(string id,Ground place,int seed,double riseM,bool mirrored,double baseYaw)
        {
            for(var attempt=0;attempt<9;attempt++)
            {
                var at=new Ground(place.X+(attempt%3-1)*14*(attempt==0?0:1),place.Z+(attempt/3)*-12);
                if(!Free(at,Outcrop.LandmarkHalfX))continue;
                var gx=(Terrain.MeshHeight(at.X+10,at.Z)-Terrain.MeshHeight(at.X-10,at.Z));
                var gz=(Terrain.MeshHeight(at.X,at.Z+10)-Terrain.MeshHeight(at.X,at.Z-10));
                // its long axis lies across its viewer's line of sight (east-west for the 16:9 HOME), turned a little with the contour
                var yaw=baseYaw+Math.Max(-18,Math.Min(18,(gx==0&&gz==0)?0:Math.Atan2(gx,gz)*180/Math.PI*.3));
                var outcrop=Outcrop.FitLandmark(Terrain,at,yaw,seed+attempt,riseM,mirrored);
                if(outcrop==null)continue;
                var near=false;
                foreach(var ring in outcrop.Rings)foreach(var q in ring)if(Clearance(new Ground(q.X,q.Z))<2.5||Reserved(new Ground(q.X,q.Z),2))near=true;
                if(near)continue;
                var footprint=outcrop.Footprint(id);
                if(!Registry.TryAdd(footprint))continue;
                props.Add(new WorldProp("Rock",footprint,at,yaw,2*Outcrop.LandmarkHalfX,2*Outcrop.LandmarkHalfZ,outcrop.Peak-outcrop.GroundAtCentre,"outcrop",outcrops.Count));
                outcrops.Add(outcrop);
                return outcrop;
            }
            return null;
        }

        /// <summary>Places one cluster; returns how many ledges were seated (the first is the largest).</summary>
        int PlaceCluster(Ground centre,int cls,int index)
        {
            var seated=0;
            var count=2+(int)(CoastalTerrain.Hash(index*13+5,cls*7+3)*3.999);
            // long axes follow the contour, so the ledge reads as a stratum rather than a boulder
            var gx=(Terrain.MeshHeight(centre.X+10,centre.Z)-Terrain.MeshHeight(centre.X-10,centre.Z));
            var gz=(Terrain.MeshHeight(centre.X,centre.Z+10)-Terrain.MeshHeight(centre.X,centre.Z-10));
            // local +x runs along (cos a, -sin a); the contour direction is (-gz, gx)
            var contourYaw=(gx==0&&gz==0)?0:Math.Atan2(gx,gz)*180/Math.PI;
            for(var i=0;i<count;i++)
            {
                for(var attempt=0;attempt<6;attempt++)
                {
                    var key=index*101+i*17+attempt*5;
                    var angle=CoastalTerrain.Hash(key,1)*2*Math.PI;
                    var dist=i==0?0:16+7*i+CoastalTerrain.Hash(key,2)*6;
                    var at=new Ground(centre.X+Math.Cos(angle)*dist,centre.Z+Math.Sin(angle)*dist);
                    var size=i==0?(cls==0?12+CoastalTerrain.Hash(key,3)*10:cls==3?20+CoastalTerrain.Hash(key,3)*15:18+CoastalTerrain.Hash(key,3)*17):8+CoastalTerrain.Hash(key,3)*8;
                    var halfX=size/2;var halfZ=halfX*(.5+.3*CoastalTerrain.Hash(key,4));
                    var yaw=contourYaw+(CoastalTerrain.Hash(key,5)-.5)*40;
                    if(!Free(at,halfX))continue;
                    var outcrop=Outcrop.Fit(Terrain,at,yaw,halfX,halfZ,key);
                    if(outcrop==null)continue;
                    var near=false;
                    foreach(var ring in outcrop.Rings)foreach(var q in ring)if(Clearance(new Ground(q.X,q.Z))<2.5||Reserved(new Ground(q.X,q.Z),2))near=true;
                    if(near)continue;
                    var footprint=outcrop.Footprint("rock:"+serial++);
                    if(!Registry.TryAdd(footprint))continue;
                    props.Add(new WorldProp("Rock",footprint,at,yaw,2*halfX,2*halfZ,outcrop.Protrusion,"outcrop",outcrops.Count));
                    outcrops.Add(outcrop);seated++;break;
                }
            }
            return seated;
        }

        // ---------------------------------------------------------------- trees

        // Own streams: the town's stream is never read here, so adding or retuning trees cannot move a house.
        void Tree(Ground p,TreeKind kind,double height,bool town)
        {
            // Max species crown extent + lean + branch lobes is below .56 * height in either horizontal axis.
            Add("Tree",p,height*1.12,height*1.12,height:height,area:town?"town":"forest",variant:(int)kind);
        }

        /// <summary>Park, yard and verge specimens (5-10 m), registered like any prop; then the forest, which keeps clear of every reservation without reserving its own crowns.</summary>
        void Trees()
        {
            var rng=new Random(4721);
            // Each pose's line of sight runs from its camera to its subject (the look target, lifted to a device's height: far views aim their look below the ground).
            var sights=StoryPoses.All().Select(n=>map.Pose(n)).Select(q=>new CameraPose(q.Camera,new Point3(q.Look.X,Math.Max(q.Look.Y,SitePlan.DeckHeightM+1.4),q.Look.Z),q.VerticalFov)).ToList();
            bool Sighted(Ground p,double height,double radius)
            {
                var ground=CoastalTerrain.Height(p.X,p.Z);
                foreach(var s in sights)if(StoryPoses.Blocks(s,p.X,p.Z,ground,ground+height,radius))return true;
                return false;
            }
            // The park's perimeter, never its lawn or its path.
            var path=new[]{new Ground(-135,80),new Ground(-150,104),new Ground(-172,122),new Ground(-198,132),new Ground(-222,156),new Ground(-236,168)};
            foreach(var q in new[]{new Ground(-233,98),new Ground(-233,126),new Ground(-214,177),new Ground(-168,177),new Ground(-127,160)})
            {
                var kind=TreeStyle.Pick(rng.NextDouble(),q.X,q.Z);var height=TreeStyle.Height(kind,rng.NextDouble(),true);
                var nearPath=double.MaxValue;for(var i=1;i<path.Length;i++)nearPath=Math.Min(nearPath,SceneryStreet.SegmentDistance(q,path[i-1],path[i]));
                if(nearPath>4.5+height*.4&&Clearance(q)>height*.56+1&&!Sighted(q,height,height*.5))Tree(q,kind,height,true);
            }
            // One yard tree for about every two houses: behind or beside the lot, clear of the street and every reservation.
            var yard=0;
            foreach(var lot in props.Where(p=>p.Kind=="House").ToList())
            {
                if(yard++%2!=0)continue;
                var kind=TreeStyle.Pick(rng.NextDouble(),lot.At.X,lot.At.Z);var height=TreeStyle.Height(kind,rng.NextDouble(),true);
                var r=height*.56;var f=lot.Footprint;
                var back=-(f.Depth/2+r+.6);var side=f.Width/2+r+.6;
                var start=(int)(rng.NextDouble()*5);
                var options=new[]{new Ground(0,back),new Ground(side,back*.5),new Ground(-side,back*.5),new Ground(side*1.1,0),new Ground(-side*1.1,0)};
                for(var k=0;k<options.Length;k++)
                {
                    var o=options[(start+k)%options.Length];var p=f.Local(o.X,o.Z);
                    if(Clearance(p)<r+1.2||Reserved(p,r)||!Free(p,r)||Sighted(p,height,r))continue;
                    if(Add("Tree",p,height*1.12,height*1.12,height:height,area:"town",variant:(int)kind))break;
                }
            }
            // The forest: broad connected stands on a jittered 8 m grid, thinned by distance and faded out at the plantation's edge.
            for(var z=-90.0;z<1900;z+=8)for(var x=-2000.0;x<2000;x+=8)
            {
                var p=new Ground(x+rng.NextDouble()*8-4,z+rng.NextDouble()*8-4);
                var keep=rng.NextDouble();var kindRoll=rng.NextDouble();var heightRoll=rng.NextDouble();
                var d=CoastalTerrain.CoreDistance(p.X,p.Z);
                if(d<1||d>TreeStyle.PlantationM)continue;
                var spacing=TreeStyle.Spacing(d);
                if(keep>(8/spacing)*(8/spacing)*TreeStyle.Fade(d))continue;
                if(!CoastalTerrain.Woodland(p.X,p.Z))continue;
                var kind=TreeStyle.Pick(kindRoll,p.X,p.Z);var height=TreeStyle.Height(kind,heightRoll,false);
                var crown=height*.45;
                if(Clearance(p)<crown+3||!Free(p,crown))continue;
                var footprint=new PropFootprint("tree:"+serial++,p,height*.9,height*.9,0);
                if(!Registry.IsFree(footprint)||Sighted(p,height,crown))continue;
                props.Add(new WorldProp("Tree",footprint,p,0,height*1.12,height*1.12,height,"forest",(int)kind));
            }
        }
        void Lamps()
        {
            foreach(var s in streets)
            {
                var side=1;
                for(var distance=10.0;distance<s.Length-4;distance+=26,side=-side)
                {
                    s.At(distance,out var p,out var t);var n=new Ground(t.Z*side,-t.X*side);
                    var c=new Ground(p.X+n.X*(s.Width/2+.9),p.Z+n.Z*(s.Width/2+.9));
                    if(Math.Abs(c.X)<240&&c.Z<140||Clearance(c)<.5||Reserved(c)||map.Anchors.Values.Any(a=>SceneryStreet.Distance(a,c)<12))continue;
                    Add("StreetLamp",c,.8,2.5,Math.Atan2(-n.X,-n.Z)*180/Math.PI);
                }
            }
        }
    }
}

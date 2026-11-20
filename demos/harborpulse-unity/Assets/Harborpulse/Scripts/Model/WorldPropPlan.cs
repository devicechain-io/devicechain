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
        public WorldProp(string kind, PropFootprint footprint, Ground at, double yaw, double width, double depth, double height=0,string area="")
        { Kind=kind;Footprint=footprint;At=at;Yaw=yaw;Width=width;Depth=depth;Height=height;Area=area; }
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
        public WorldProp Find(string id) => props.FirstOrDefault(p=>p.Footprint.Id==id);
        readonly PresentationMap map;
        readonly Random random=new Random(1789);
        int serial;
        WorldPropPlan(PresentationMap map) { this.map=map; }
        public static WorldPropPlan Generate(PresentationMap map,HarborLayout layout)
        {
            var p=new WorldPropPlan(map);
            p.AddStreets(); p.Devices(layout); p.Waterfront(); p.Lots(); p.Rocks(); p.Trees(); p.Lamps();
            return p;
        }
        bool Add(string kind,Ground at,double w,double d,double yaw=0,double height=0,string area="",string id=null,double apron=0)
        {
            var footprint=new PropFootprint(id??kind+":"+serial++,at,w+apron,d+apron,yaw);
            if(!Registry.TryAdd(footprint)) return false;
            props.Add(new WorldProp(kind,footprint,at,yaw,w,d,height,area));return true;
        }
        public static double QuayYaw(string token) => token=="hp-north-quay" ? 180 : token=="hp-east-quay" ? 270 : 90;
        public static PropFootprint QuayFrame(PresentationMap map,string token)
        {
            var q=map.Quays[token];var x0=q.Min(p=>p.X);var x1=q.Max(p=>p.X);var z0=q.Min(p=>p.Z);var z1=q.Max(p=>p.Z);
            var north=token=="hp-north-quay";var east=token=="hp-east-quay";
            return new PropFootprint(token,north?new Ground((x0+x1)/2,z0):new Ground(east?x0:x1,(z0+z1)/2),north?x1-x0:z1-z0,north?z1-z0:x1-x0,QuayYaw(token));
        }
        void Devices(HarborLayout layout)
        {
            foreach(var device in layout.Devices)
            {
                var at=map.Anchors[device.Token];var yaw=device.Kind==DeviceKind.Truck?0:QuayYaw(map.QuayFor(device));
                var frame=new PropFootprint(device.Token,at,1,1,yaw);
                void Fixed(string suffix,double x,double z,double w,double d) => Registry.AddFixed(new PropFootprint("device:"+device.Token+suffix,frame.Local(x,z),w,d,yaw));
                switch(device.Kind)
                {
                    case DeviceKind.Meter: Fixed("",0,0,device.Index==1?39.2:57,device.Index==1?19.2:23);break;
                    case DeviceKind.Pump:
                        Fixed("",-.7,-.7,11.8,8.6); Fixed(":well",WellFootprint.LocalX,WellFootprint.LocalZ,5.6,5.6);break;
                    case DeviceKind.Truck: Fixed("",0,0,3.4,7.6);break;
                    case DeviceKind.Light: Fixed("",0,.8,.8,2.8);break;
                    case DeviceKind.Charger: Fixed("",0,.55,1.9,2.1);break;
                    default:
                        Fixed("",0,0,1.5,1.5);
                        var token=map.QuayFor(device);var wall=QuayFrame(map,token);
                        var localZ=(at.X-wall.At.X)*Math.Sin(yaw*Math.PI/180)+(at.Z-wall.At.Z)*Math.Cos(yaw*Math.PI/180);
                        var lateral=token=="hp-north-quay"?0:device.Kind==DeviceKind.LoraGauge?-10:device.Kind==DeviceKind.LegacyGauge?-7:(device.Index-2)*3;
                        Fixed(":staff",lateral,-localZ+.64,.6,.2);
                        break;
                }
            }
            foreach(var device in layout.Devices.Where(d=>d.Kind==DeviceKind.Charger))
            {
                var frame=new PropFootprint(device.Token,map.Anchors[device.Token],1,1,QuayYaw(map.QuayFor(device)));
                var placed=false;
                foreach(var z in new[]{4.0,7.0})
                {
                    foreach(var x in new[]{0.0,-3.0,3.0,-6.0,6.0})
                        if(Add("ChargingCar",frame.Local(x,z),CarWidth,CarDepth,frame.Yaw,area:device.Token,id:"car:"+device.Token)) { placed=true; break; }
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
        void Rocks()
        {
            // Exposed shelves sit below the skyline, where HOME can read their lit upper faces.
            foreach(var p in new[]{new Ground(-270,160),new Ground(270,155),new Ground(-360,40),new Ground(360,30),
                new Ground(-330,840),new Ground(140,930),new Ground(-170,1130),new Ground(100,1190)})
            {
                var radius=p.Z<200?28:38;
                if(Free(p,radius)) Add("Rock",p,radius*2.7,radius*2.7,height:radius);
            }
            for(var z=-100;z<1600;z+=72)for(var x=-1500;x<1500;x+=81)
            {
                var h=CoastalTerrain.Hash(x,z);var shore=z<-20&&Math.Abs(x)>280&&Math.Abs(x)<1000;
                var upper=CoastalTerrain.Height(x,z)>52&&CoastalTerrain.Clearing(x,z);
                if((!shore&&!upper)||h>(shore?.34:.2))continue;
                var radius=shore?18+h*22:24+h*32;
                // Both granite lobes, including the rotated second lobe, inside this conservative envelope.
                Add("Rock",new Ground(x,z),radius*2.7,radius*2.7,height:radius);
            }
        }
        void Tree(Ground p,double height,bool town)
        {
            // Max species crown extent + lean + branch lobes is below .56 * height in either horizontal axis.
            Add("Tree",p,height*1.12,height*1.12,height:height,area:town?"town":"forest");
        }
        void Trees()
        {
            foreach(var p in new[]{new Ground(-196,110),new Ground(-174,150),new Ground(-214,118),new Ground(-150,140)})Tree(p,11+random.NextDouble()*4,true);
            var candidate=0;
            for(var z=-120.0;z<520;z+=11)for(var x=-470.0;x<470;x+=11)
            {
                var p=new Ground(x+random.NextDouble()*9-4.5,z+random.NextDouble()*9-4.5);var side=p.Z<146;
                if(side&&Math.Abs(p.X)<244)continue;
                var density=side?.2+.5*CoastalTerrain.Noise(p.X*.017+7,p.Z*.017)*CoastalTerrain.Smooth(-140,-80,p.Z)
                    :.12+.4*CoastalTerrain.Noise(p.X*.02,p.Z*.02)+CoastalTerrain.Smooth(300,500,p.Z)*.5+CoastalTerrain.Smooth(260,460,Math.Abs(p.X))*.3;
                if(random.NextDouble()>density || Clearance(p)<7 || !Free(p,5))continue;
                var height=8+random.NextDouble()*7;
                // Yard specimens stay small; the woodland edge grows into connected stands.
                if(p.Z < 300 && Math.Abs(p.X) < 340)
                { if(CoastalTerrain.KeepTree(candidate++))Tree(p,11+random.NextDouble()*6,true); }
                else if(CoastalTerrain.Woodland(p.X,p.Z))Tree(p,17+random.NextDouble()*7,false);
            }
            for(var z=-120.0;z<2250;z+=13)for(var x=-2250.0;x<2250;x+=13)
            {
                if(Math.Abs(x)<470&&z<520)continue;
                var step=13+Math.Max(0,CoastalTerrain.CoreDistance(x,z)-470)*.05;
                if(random.NextDouble()>(13/step)*(13/step))continue;
                var p=new Ground(x+(random.NextDouble()-.5)*step,z+(random.NextDouble()-.5)*step);
                if(!CoastalTerrain.Woodland(p.X,p.Z))continue;
                var height=20+random.NextDouble()*12;
                if(p.Z-height*.47<-136||Math.Abs(p.X)+height*.47>3400)continue;
                if(CoastalTerrain.ForestFits(p.X,p.Z,height)&&Free(p,height*.47))Tree(p,height,false);
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

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>WORLD navigation marks: no platform severity or device identity is inferred here.</summary>
    public static class NavigationBuilder
    {
        public static void Beacon(Vector3 at, Transform parent, HarborMaterials m, bool green)
        {
            var masonry = new MeshKit().Cylinder(at,2.1f,.35f,10)
                .Cylinder(at+Vector3.up*.35f,2,11.65f,10,1.2f)
                .Cylinder(at+Vector3.up*11.7f,1.8f,.25f,10)
                .Cylinder(at+Vector3.up*13.8f,1.65f,.16f,10,1.5f)
                .Cylinder(at+Vector3.up*13.96f,1.5f,.8f,10,0);
            masonry.Spawn("Light tower",parent,m.Render);
            // Lantern colour and the channel side remain unchanged; the narrow frame reveals the glass inside.
            new MeshKit().Cylinder(at+Vector3.up*12,1.3f,1.8f,10)
                .Cylinder(at+Vector3.up*7.8f,1.55f,.65f,10,1.5f)
                .Spawn("Navigation light and band",parent,green?m.BuoyGreen:m.BuoyRed);
            var iron = new MeshKit().Box(at+new Vector3(0,1.1f,-1.93f),new Vector3(.85f,1.7f,.13f))
                .Hoop(at+Vector3.up*12.8f,Vector3.up,1.75f,.07f,10);
            for (var i=0;i<10;i++)
            {
                var a=i*Mathf.PI*2/10;var d=new Vector3(Mathf.Cos(a),0,Mathf.Sin(a));
                iron.Beam(at+d*1.7f+Vector3.up*12,at+d*1.7f+Vector3.up*12.8f,.055f)
                    .Beam(at+d*1.34f+Vector3.up*12,at+d*1.34f+Vector3.up*13.8f,.07f);
            }
            iron.Spawn("Gallery lantern frame and door",parent,m.DarkSteel);
        }

        public static Transform Buoy(Transform parent, HarborMaterials m, bool can)
        {
            var kit=new MeshKit().Cylinder(new Vector3(0,-.8f,0),.8f,2.6f,8,can?.8f:.05f)
                .Cylinder(new Vector3(0,.1f,0),.86f,.16f,8);
            // Cage/ladder on a can, spar and conical topmark on a nun.
            for (var s=-1;s<=1;s+=2)
                kit.Beam(new Vector3(s*.27f,1.6f,0),new Vector3(s*.27f,3.1f,0),.055f);
            for (var y=1.8f;y<3.1f;y+=.3f)
                kit.Beam(new Vector3(-.27f,y,0),new Vector3(.27f,y,0),.045f);
            if(can) kit.Hoop(new Vector3(0,3.1f,0),Vector3.up,.38f,.055f)
                .Cylinder(new Vector3(0,3.3f,0),.25f,.36f,6);
            else kit.Cylinder(new Vector3(0,3.1f,0),.36f,.7f,6,0);
            var root=kit.Spawn("Channel buoy",parent,can?m.BuoyRed:m.BuoyGreen).transform;
            // Two broad links suggest the mooring chain without dozens of tiny torus meshes.
            new MeshKit().Hoop(new Vector3(0,-1,0),Vector3.forward,.15f,.045f,6)
                .Beam(new Vector3(0,-1.15f,0),new Vector3(.22f,-2.5f,.08f),.06f)
                .Spawn("Mooring chain",root,m.DarkSteel,false);
            return root;
        }
    }
}

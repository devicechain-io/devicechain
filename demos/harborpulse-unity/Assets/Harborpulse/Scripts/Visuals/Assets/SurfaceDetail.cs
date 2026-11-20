// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>Small merged markings; alpha selects the shared scenery shader's procedural surface treatment.</summary>
    internal static class SurfaceDetail
    {
        public static void Colour(GameObject go, Color colour)
        {
            colour = colour.linear; // RGB only; alpha retains the procedural surface selector.
            var mesh=go.GetComponent<MeshFilter>().sharedMesh;
            var colours=new Color[mesh.vertexCount];
            for(var i=0;i<colours.Length;i++)colours[i]=colour;
            mesh.colors=colours;
        }
        static void Stripe(SceneryMesh mesh,Vector3 at,Vector3 size,Quaternion rotation)
            => mesh.Box(at,size,new Color(.64f,.61f,.48f),rotation);
        public static void Quay(Transform parent,HarborMaterials m,QuayFrame frame)
        {
            var mesh=new SceneryMesh();var z=-frame.Depth+5.5f;var y=(float)Model.SitePlan.DeckHeightM+.07f;
            for(var x=-frame.Length/2+3;x<frame.Length/2-3;x+=9)
                Stripe(mesh,new Vector3(x,y,z),new Vector3(3,.018f,.11f),Quaternion.identity);
            mesh.Spawn("Faded quay lane markings",parent,m.Surfaces);
        }
        public static void Parking(Transform parent,HarborMaterials m,float x0,float x1,float z0,float z1,float y)
        {
            var mesh=new SceneryMesh();var z=(z0+z1)/2;
            for(var x=x0+1.5f;x<x1-1;x+=7)
                Stripe(mesh,new Vector3(x,y,z),new Vector3(.09f,.016f,5.4f),Quaternion.identity);
            Stripe(mesh,new Vector3((x0+x1)/2,y,z+2.7f),new Vector3(x1-x0-2,.016f,.09f),Quaternion.identity);
            mesh.Spawn("Faded parking bays",parent,m.Surfaces);
        }
        public static void Road(Transform parent,HarborMaterials m,Vector3 a,Vector3 b)
        {
            var mesh=new SceneryMesh();var d=b-a;var length=d.magnitude;
            var rotation=Quaternion.Euler(0,Mathf.Atan2(d.x,d.z)*Mathf.Rad2Deg,0);
            for(var s=3f;s<length-2;s+=8)
                Stripe(mesh,a+d*(s/length)+Vector3.up*.045f,new Vector3(.1f,.016f,2.5f),rotation);
            if(mesh.VertexCount>0)mesh.Spawn("Depot road centre dashes",parent,m.Surfaces);
        }
    }
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>
    /// The far level's architectural geometry, written into shared town tiles: the stone base, the corner boards and the door frame. The fine
    /// detail (window frames and sashes, shutters, clapboard courses, steps, rails) is the near level's, built per building by the detail
    /// manager from the model's near plans, so it costs nothing at HOME.
    /// </summary>
    internal static class BuildingDetail
    {
        static readonly Color Trim = new Color(.86f,.84f,.77f);
        static readonly Color Granite = new Color(.49f,.47f,.44f);
        public static void Envelope(SceneryMesh mesh, Vector3 at,float yaw,float w,float d,float h,Color paint,bool clapboard,bool rich,float doorX)
        {
            var r=Quaternion.Euler(0,yaw,0);
            void Box(Vector3 p,Vector3 s,Color c) => mesh.Box(at+r*p,s,c,r);
            Box(new Vector3(0,.13f,0),new Vector3(w+.24f,.4f,d+.24f),Granite);
            if(!rich) return;
            foreach(var x in new[]{-1,1}) foreach(var z in new[]{-1,1})
                Box(new Vector3(x*(w/2+.025f),h/2,z*(d/2+.025f)),new Vector3(.2f,h,.2f),Trim);
            foreach(var x in new[]{-1,1}) Box(new Vector3(doorX+x*.67f,1.15f,-d/2-.12f),new Vector3(.18f,2.45f,.2f),Trim);
            Box(new Vector3(doorX,2.4f,-d/2-.12f),new Vector3(1.52f,.18f,.2f),Trim);
        }

        public static void Roof(MeshKit mesh,float w,float d,float h,float rise,int style)
        {
            var a=new Vector3(-w/2,h,-d/2); var b=new Vector3(w/2,h,-d/2);
            var c=new Vector3(w/2,h,d/2); var e=new Vector3(-w/2,h,d/2);
            var l=new Vector3(-w/2,h+rise,0); var r=new Vector3(w/2,h+rise,0);
            if(style==1) // gambrel: steep lower pitches, shallow upper slopes
            {
                var a1=a+new Vector3(0,rise*.7f,d*.18f); var b1=b+new Vector3(0,rise*.7f,d*.18f);
                var c1=c+new Vector3(0,rise*.7f,-d*.18f); var e1=e+new Vector3(0,rise*.7f,-d*.18f);
                mesh.Quad(a,a1,b1,b); mesh.Quad(a1,l,r,b1); mesh.Quad(c,c1,e1,e); mesh.Quad(c1,r,l,e1);
                mesh.Quad(a,e,e1,a1); mesh.Quad(a1,e1,l,l); mesh.Quad(b,b1,c1,c); mesh.Quad(b1,r,c1,c1);
            }
            else if(style==2) // hip, with a shorter ridge
            {
                l.x+=w*.24f; r.x-=w*.24f;
                mesh.Quad(a,l,r,b); mesh.Quad(c,r,l,e); mesh.Quad(a,e,l,l); mesh.Quad(b,r,c,c);
            }
            else if(style==3) // saltbox, unequal rear and front roof slopes
            {
                l.z=-d*.18f; r.z=l.z;
                mesh.Quad(a,l,r,b); mesh.Quad(c,r,l,e); mesh.Quad(a,e,l,l); mesh.Quad(b,r,c,c);
            }
            else
            {
                mesh.Gable(new Vector3(0,h,0),w,d,rise);
                if(style==4) mesh.Gable(new Vector3(w*.18f,h,-d*.12f),d*.8f,w*.48f,rise*.83f,90);
            }
            var capZ=style==3 ? -d*.18f : 0;
            mesh.Box(new Vector3(0,h+rise+.03f,capZ),new Vector3(style==2 ? w*.52f : w+.08f,.17f,.23f));
            mesh.Box(new Vector3(0,h-.06f,-d/2),new Vector3(w,.17f,.2f));
            mesh.Box(new Vector3(0,h-.06f,d/2),new Vector3(w,.17f,.2f));
        }
    }
}

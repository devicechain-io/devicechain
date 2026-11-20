// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>Small architectural geometry written into shared town tiles, with a fixed distance detail tier.</summary>
    internal static class BuildingDetail
    {
        static readonly Color Trim = new Color(.86f,.84f,.77f);
        static readonly Color Granite = new Color(.49f,.47f,.44f);
        public static void Window(SceneryMesh mesh, Vector3 at, float yaw, Vector3 center, Vector3 along, Vector3 face, Vector2 size, bool rich)
        {
            var r = Quaternion.Euler(0,yaw,0);
            Vector3 P(float x,float y) => at+r*(center+along*x+Vector3.up*y+face*.085f);
            void Panel(float x0,float x1,float y0,float y1,Color c) => mesh.Quad(P(x0,y0),P(x0,y1),P(x1,y1),P(x1,y0),c);
            // Two-sided facade axes have opposite winding. Boxes for the narrow frame keep every facade visible.
            var w = size.x*.5f; var h = size.y*.5f; const float t=.12f;
            var horizontal = new Vector3(Mathf.Abs(along.x)*(size.x+.28f)+Mathf.Abs(face.x)*.14f,.12f,Mathf.Abs(along.z)*(size.x+.28f)+Mathf.Abs(face.z)*.14f);
            mesh.Box(at+r*(center+face*.08f+Vector3.up*(h+.06f)),horizontal,Trim,r);
            mesh.Box(at+r*(center+face*.13f-Vector3.up*(h+.08f)),horizontal+new Vector3(Mathf.Abs(face.x)*.25f,.04f,Mathf.Abs(face.z)*.25f),Trim,r);
            if (!rich) return;
            foreach(var s in new[]{-1,1})
            {
                var side = new Vector3(Mathf.Abs(along.x)*t+Mathf.Abs(face.x)*.14f,size.y,Mathf.Abs(along.z)*t+Mathf.Abs(face.z)*.14f);
                mesh.Box(at+r*(center+along*(s*(w+.08f))+face*.08f),side,Trim,r);
                if (face.z < 0)
                {
                    var shutter = new Vector3(.34f,size.y+.1f,.1f);
                    mesh.Box(at+r*(center+along*(s*(w+.37f))+face*.1f),shutter,new Color(.25f,.32f,.28f),r);
                }
            }
            // One cross rail rather than a grid of invisible individual panes.
            if(face.z < 0) Panel(-w,w,-.035f,.035f,Trim);
        }

        public static void Envelope(SceneryMesh mesh, Vector3 at,float yaw,float w,float d,float h,Color paint,bool clapboard,bool rich,float doorX)
        {
            var r=Quaternion.Euler(0,yaw,0);
            void Box(Vector3 p,Vector3 s,Color c) => mesh.Box(at+r*p,s,c,r);
            Box(new Vector3(0,.13f,0),new Vector3(w+.24f,.4f,d+.24f),Granite);
            if(!rich) return;
            foreach(var x in new[]{-1,1}) foreach(var z in new[]{-1,1})
                Box(new Vector3(x*(w/2+.025f),h/2,z*(d/2+.025f)),new Vector3(.2f,h,.2f),Trim);
            if(clapboard)
            {
                // Broad weathered courses read at HOME; close views reveal individual board edges.
                for(var y=.65f;y<h;y+=.65f)
                {
                    var shade=paint*(.88f+.08f*Mathf.Sin(y*7));
                    foreach(var z in new[]{-1,1})
                    {
                        Vector3 P(float x,float yy) => at+r*new Vector3(x,yy,z*(d/2+.015f));
                        if(z<0) mesh.Quad(P(-w/2,y),P(-w/2,y+.07f),P(w/2,y+.07f),P(w/2,y),shade);
                        else mesh.Quad(P(w/2,y),P(w/2,y+.07f),P(-w/2,y+.07f),P(-w/2,y),shade);
                    }
                }
            }
            foreach(var x in new[]{-1,1}) Box(new Vector3(doorX+x*.67f,1.15f,-d/2-.12f),new Vector3(.18f,2.45f,.2f),Trim);
            Box(new Vector3(doorX,2.4f,-d/2-.12f),new Vector3(1.52f,.18f,.2f),Trim);
            for(var i=0;i<3;i++) Box(new Vector3(doorX,.10f+i*.14f,-d/2-.65f+(i-1)*.30f),new Vector3(1.9f,.2f+i*.28f,.34f),Granite);
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

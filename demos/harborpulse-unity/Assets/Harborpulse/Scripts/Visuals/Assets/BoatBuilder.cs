// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>Refines the authored moorings in place. Yaw stays baked into the meshes, so the existing wave motion owns the root.</summary>
    public static class BoatBuilder
    {
        public static void Refine(Transform boat, HarborMaterials m, int slot)
        {
            var hull = boat.Find("Hull");
            if (hull == null || hull.GetComponent<MeshFilter>() == null) return;
            var old = hull.GetComponent<MeshFilter>().sharedMesh;
            var points = old.vertices;
            if (points.Length < 4) return;
            var forward = points[2] - points[1]; forward.y = 0; forward.Normalize();
            var right = Vector3.Cross(Vector3.up, forward);
            float minZ = float.MaxValue, maxZ = float.MinValue, minX = float.MaxValue, maxX = float.MinValue;
            foreach (var p in points)
            {
                var z = Vector3.Dot(p, forward); var x = Vector3.Dot(p, right);
                minZ = Mathf.Min(minZ,z); maxZ = Mathf.Max(maxZ,z); minX = Mathf.Min(minX,x); maxX = Mathf.Max(maxX,x);
            }
            var length = maxZ-minZ; var beam = maxX-minX;
            var yaw = Mathf.Atan2(forward.x,forward.z)*Mathf.Rad2Deg;
            var sail = boat.Find("Mast") != null;
            var ferry = !sail && slot % 8 == 6;
            var skiff = !sail && (slot % 8 == 4 || slot % 8 == 7);
            var lobster = !sail && slot % 8 == 2;
            var shell = new MeshKit().Place(Vector3.zero,yaw);
            var white = new MeshKit().Place(Vector3.zero,yaw);
            var dark = new MeshKit().Place(Vector3.zero,yaw);
            var timber = new MeshKit().Place(Vector3.zero,yaw);
            // Fishing boats have a broad working stern; yachts finer shoulders; skiffs a shallow, open hull.
            ChinedHull(shell,length,beam,skiff?.48f:1.2f,skiff?.72f:.88f,sail?.18f:.32f);
            var deckY = skiff ? .34f : 1.22f;
            var deckHalfBeam = beam * (skiff ? .32f : .42f);
            timber.Slab(-deckHalfBeam,deckHalfBeam,-length*.44f,length*.20f,deckY-.10f,deckY);
            if (sail)
            {
                white.Box(new Vector3(0,1.45f,-length*.10f),new Vector3(beam*.59f,.65f,length*.30f))
                    .Beam(new Vector3(0,1.4f,length*.1f),new Vector3(0,length*.55f+.6f,length*.1f),.14f)
                    .Beam(new Vector3(0,2.05f,length*.1f),new Vector3(0,2.05f,-length*.32f),.11f)
                    .Box(new Vector3(0,2.17f,-length*.11f),new Vector3(.24f,.2f,length*.40f));
                dark.Box(new Vector3(0,1.5f,-length*.1f),new Vector3(beam*.61f,.24f,length*.24f));
                var masthead = new Vector3(0,length*.55f+.5f,length*.1f);
                dark.Beam(masthead,new Vector3(0,1.25f,length*.47f),.025f)
                    .Beam(masthead,new Vector3(0,1.25f,-length*.46f),.025f);
                for (var s=-1;s<=1;s+=2)
                    dark.Beam(masthead,new Vector3(s*beam*.44f,1.25f,0),.025f);
                // Ties break up a furled mainsail without a large opaque sail in the HOME sightline.
                for (var z=-length*.29f;z<length*.08f;z+=length*.09f)
                    dark.Box(new Vector3(0,2.17f,z),new Vector3(.255f,.21f,.035f));
            }
            else if (skiff)
            {
                for (var z=-length*.28f;z<length*.30f;z+=length*.25f)
                    timber.Box(new Vector3(0,.73f,z),new Vector3(beam*.86f,.13f,.35f));
                dark.Box(new Vector3(0,.7f,-length*.48f),new Vector3(.45f,.8f,.35f))
                    .Beam(new Vector3(-beam*.35f,.81f,-length*.15f),new Vector3(beam*.3f,.81f,length*.25f),.055f);
            }
            else
            {
                var cabinZ = lobster ? length*.10f : -length*.06f;
                var cabinLength = length*(ferry?.65f:.30f);
                white.Box(new Vector3(0,1.91f,cabinZ),new Vector3(beam*.66f,1.35f,cabinLength))
                    .Box(new Vector3(0,2.63f,cabinZ),new Vector3(beam*.77f,.13f,cabinLength+.35f));
                dark.Box(new Vector3(0,2.17f,cabinZ),new Vector3(beam*.68f,.52f,cabinLength*.85f));
                // Mullions cut the continuous glazing into distinct wheelhouse panes.
                for (var s=-1;s<=1;s+=2)
                    white.Box(new Vector3(s*beam*.34f,2.17f,cabinZ),new Vector3(.065f,.57f,.07f));
                if (ferry)
                {
                    white.Box(new Vector3(0,3.05f,0),new Vector3(beam*.56f,.7f,length*.38f))
                        .Box(new Vector3(0,3.46f,0),new Vector3(beam*.66f,.12f,length*.42f));
                    dark.Box(new Vector3(0,3.1f,0),new Vector3(beam*.58f,.34f,length*.32f));
                    shell.Box(new Vector3(0,3.1f,-length*.26f),new Vector3(.65f,.9f,.75f));
                }
                else
                {
                    white.Beam(new Vector3(-beam*.35f,1.3f,-length*.34f),new Vector3(-beam*.35f,3.5f,-length*.34f),.09f)
                        .Beam(new Vector3(-beam*.35f,3.5f,-length*.34f),new Vector3(beam*.35f,3.5f,-length*.34f),.09f);
                    dark.Beam(new Vector3(beam*.35f,3.5f,-length*.34f),new Vector3(beam*.35f,1.3f,-length*.34f),.025f);
                    Trap(timber,new Vector3(0,1.32f,-length*.31f),beam*.55f);
                    if (lobster) Trap(timber,new Vector3(0,1.91f,-length*.31f),beam*.55f);
                }
            }
            // Cleats, fenders and gunwale rails are baked into the original four material batches.
            for (var s=-1;s<=1;s+=2)
            {
                white.Beam(new Vector3(s*beam*.48f,deckY+.25f,-length*.47f),new Vector3(s*beam*.48f,deckY+.25f,length*.13f),.07f);
                for (var z=-length*.3f;z<=length*.25f;z+=length*.5f)
                {
                    dark.Cylinder(new Vector3(s*beam*.5f,deckY-.35f,z),.13f,.55f,6);
                    white.Box(new Vector3(s*beam*.4f,deckY+.08f,z),new Vector3(.3f,.1f,.10f));
                }
                if (ferry) for (var z=-length*.36f;z<length*.36f;z+=length*.18f)
                    white.Beam(new Vector3(s*beam*.45f,1.25f,z),new Vector3(s*beam*.45f,1.85f,z),.045f);
            }
            Replace(hull,shell);
            Replace(boat.Find("Cabin"),white);
            Replace(boat.Find("CabinGlass"),dark);
            Replace(boat.Find("Deck"),timber);
            var mast = boat.Find("Mast");
            if (mast != null)
            {
                var filter = mast.GetComponent<MeshFilter>();
                if (filter != null) { MeshKit.Release(filter.sharedMesh); filter.sharedMesh = null; }
                mast.gameObject.SetActive(false); MeshKit.Release(mast.gameObject);
            }
        }

        static void Replace(Transform node, MeshKit kit)
        {
            if (node == null) return;
            var filter = node.GetComponent<MeshFilter>();
            if (filter == null) return;
            MeshKit.Release(filter.sharedMesh); filter.sharedMesh = kit.Build(node.name);
        }

        static void ChinedHull(MeshKit kit, float length, float beam, float freeboard, float stern, float shoulder)
        {
            var plan = new[] {new Vector2(beam*.5f*stern,-length*.5f),new Vector2(beam*.5f,length*shoulder),
                new Vector2(0,length*.5f),new Vector2(-beam*.5f,length*shoulder),new Vector2(-beam*.5f*stern,-length*.5f)};
            for (var i=0;i<plan.Length;i++)
            {
                var a=plan[i];var b=plan[(i+1)%plan.Length];
                var topA=new Vector3(a.x,freeboard,a.y);var topB=new Vector3(b.x,freeboard,b.y);
                var chineA=new Vector3(a.x*.84f,-.08f,a.y*.97f);var chineB=new Vector3(b.x*.84f,-.08f,b.y*.97f);
                var keelA=new Vector3(a.x*.45f,-.9f,a.y*.88f);var keelB=new Vector3(b.x*.45f,-.9f,b.y*.88f);
                kit.Quad(chineA,topA,topB,chineB);kit.Quad(keelA,chineA,chineB,keelB);
                kit.Quad(keelA,keelB,new Vector3(0,-.9f,0),new Vector3(0,-.9f,0));
            }
        }

        static void Trap(MeshKit kit, Vector3 at, float width)
        {
            for (var s=-1;s<=1;s+=2)
            {
                kit.Beam(at+new Vector3(s*width*.5f,0,-.35f),at+new Vector3(s*width*.5f,.52f,-.2f),.045f)
                    .Beam(at+new Vector3(s*width*.5f,.52f,-.2f),at+new Vector3(s*width*.5f,.52f,.2f),.045f)
                    .Beam(at+new Vector3(s*width*.5f,.52f,.2f),at+new Vector3(s*width*.5f,0,.35f),.045f)
                    .Beam(at+new Vector3(-width*.5f,.52f,s*.2f),at+new Vector3(width*.5f,.52f,s*.2f),.04f);
            }
            for (var x=-width*.4f;x<width*.5f;x+=width*.2f)
                kit.Beam(at+new Vector3(x,.52f,-.2f),at+new Vector3(x,.52f,.2f),.025f);
        }
    }
}

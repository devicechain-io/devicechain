// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>Shared ambient fittings for the two authored timber piers, clear of the mapped devices.</summary>
    public static class PierDetailBuilder
    {
        public static void Build(Transform parent, HarborMaterials m)
        {
            var wood = new MeshKit(); var iron = new MeshKit(); var rope = new MeshKit();
            Pier(wood,iron,rope,new Vector3(140,0,-70),270,46,4.2f);
            Pier(wood,iron,rope,new Vector3(-155,0,-102),90,42,4.2f);
            wood.Spawn("Pier cross bracing",parent,m.Piling);
            iron.Spawn("Pier seams ladders and fenders",parent,m.DarkSteel,false);
            rope.Spawn("Pier mooring ropes",parent,m.Timber,false);
        }

        static void Pier(MeshKit wood, MeshKit iron, MeshKit rope, Vector3 at, float yaw, float length, float width)
        {
            wood.Place(at,yaw);iron.Place(at,yaw);rope.Place(at,yaw);
            var deck=(float)Model.SitePlan.DeckHeightM-.2f;
            // Shadow seams on the existing deck; actual gapped plank geometry is used on the quay jetties.
            for (var z=.65f;z<length;z+=.65f)
                iron.Quad(new Vector3(-width*.5f,deck+.003f,z),new Vector3(-width*.5f,deck+.003f,z+.035f),
                    new Vector3(width*.5f,deck+.003f,z+.035f),new Vector3(width*.5f,deck+.003f,z));
            for (var s=-1;s<=1;s+=2)
            {
                for (var z=3f;z<length-5;z+=10)
                {
                    var x=s*(width*.5f-.1f);
                    wood.Beam(new Vector3(x,-1.2f,z),new Vector3(x,deck-.4f,z+5),.19f)
                        .Beam(new Vector3(x,deck-.4f,z),new Vector3(x,-1.2f,z+5),.19f);
                }
                for (var z=8f;z<length;z+=18)
                {
                    iron.Hoop(new Vector3(s*(width*.5f+.16f),deck-.55f,z),Vector3.right,.38f,.12f)
                        .Beam(new Vector3(s*width*.5f,deck,z),new Vector3(s*(width*.5f+.16f),deck-.25f,z),.045f);
                    rope.Hoop(new Vector3(s*(width*.5f-.7f),deck+.045f,z),Vector3.up,.32f,.065f);
                }
            }
            var ladderZ=length-4;
            for (var z=-1;z<=1;z+=2)
                iron.Beam(new Vector3(width*.5f+.17f,-1.3f,ladderZ+z*.3f),new Vector3(width*.5f+.17f,deck+.55f,ladderZ+z*.3f),.055f);
            for (var y=-1.2f;y<deck+.5f;y+=.35f)
                iron.Beam(new Vector3(width*.5f+.17f,y,ladderZ-.3f),new Vector3(width*.5f+.17f,y,ladderZ+.3f),.045f);
        }
    }
}

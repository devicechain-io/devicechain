// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    internal static class TerrainBuilder
    {
        public static void Build(Transform parent, Material material, List<float> xs, List<float> zs, Func<float, float, float> height, WorldPropPlan plan)
        {
            var tiles = new Dictionary<(int, int), SceneryMesh>();
            SceneryMesh Tile(float x, float z)
            {
                var key = (Mathf.FloorToInt(Mathf.Clamp(x,-2399,2399) / 900), Mathf.FloorToInt(Mathf.Min(z,2399) / 900));
                if (!tiles.TryGetValue(key, out var mesh)) tiles[key] = mesh = new SceneryMesh();
                return mesh;
            }
            Vector3 P(float x, float z) => new Vector3(x, height(x, z), z);
            for (var j = 0; j < zs.Count - 1; j++)
                for (var i = 0; i < xs.Count - 1; i++)
                {
                    var x = (xs[i] + xs[i + 1]) * .5f; var z = (zs[j] + zs[j + 1]) * .5f;
                    if (Mathf.Abs(x) < TownBuilder.CoreHalfX && z < TownBuilder.CoreNorthZ) continue;
                    var a = P(xs[i], zs[j]); var b = P(xs[i + 1], zs[j]);
                    var c = P(xs[i], zs[j + 1]); var d = P(xs[i + 1], zs[j + 1]);
                    var n = Vector3.Cross(c - a, d - a).normalized;
                    var bracken = (float)CoastalTerrain.Noise(x * .008 + 4, z * .008);
                    var grass = Color.Lerp(new Color(.40f,.43f,.33f), new Color(.49f,.50f,.38f), bracken);
                    var inland = (float)CoastalTerrain.CoreDistance(x,z);
                    var rust = (float)CoastalTerrain.Smooth(.43,.72,bracken) * (float)CoastalTerrain.Smooth(260,600,inland);
                    grass = Color.Lerp(grass,new Color(.55f,.47f,.34f),rust*.82f);
                    var flecks = (float)CoastalTerrain.Smooth(.76,.92,bracken);
                    grass = Color.Lerp(grass,new Color(.48f,.38f,.29f),flecks*.22f);
                    // Small worn-earth aprons, with green meadow remaining between the streets and houses.
                    if (inland < 320)
                    {
                        var edge = float.MaxValue;
                        foreach(var street in plan.Streets) edge = Mathf.Min(edge,(float)(street.DistanceTo(new Ground(x,z))-street.Width/2));
                        grass = Color.Lerp(grass,new Color(.37f,.32f,.23f),1-Mathf.SmoothStep(0,1,edge/3));
                    }
                    var bare = Mathf.Clamp01((.97f - n.y) * 6 + (height(x,z) - 85) / 38);
                    var color = Color.Lerp(grass, new Color(.60f,.56f,.54f), bare*.65f);
                    // Flat facets show the spurs and the saddle; colour stays continuous across tile boundaries.
                    var kit = Tile(x,z);
                    kit.Quad(a,c,d,b,color * (.97f + .06f * (float)CoastalTerrain.Hash(i,j)));
                }
            // Verges share the terrain tiles: widening Main Street adds no renderer or shadow caster.
            foreach(var street in plan.Streets)
            {
                var steps = Math.Max(1,(int)Math.Ceiling(street.Length/8));
                foreach(var side in new[]{-1,1})
                {
                    Vector3 inner=default,outer=default;
                    for(var i=0;i<=steps;i++)
                    {
                        street.At(street.Length*i/steps,out var p,out var t);
                        var width = street.Name=="Main Street" ? 2.0 : 1.1;
                        var offset = street.Width*.5;
                        var a=P((float)(p.X+t.Z*side*offset),(float)(p.Z-t.X*side*offset))+Vector3.up*.07f;
                        var b=P((float)(p.X+t.Z*side*(offset+width)),(float)(p.Z-t.X*side*(offset+width)))+Vector3.up*.07f;
                        if(i>0)
                        {
                            var kit=Tile((float)p.X,(float)p.Z);
                            var color=new Color(.43f,.46f,.34f);
                            if(side>0)kit.Quad(inner,a,b,outer,color);else kit.Quad(outer,b,a,inner,color);
                        }
                        inner=a;outer=b;
                    }
                }
            }
            // Sparse shore shelves and upper-slope granite. No new mesh renderer per rock.
            foreach(var rock in plan.Props)
            {
                if(rock.Kind!="Rock")continue;
                var x=(float)rock.At.X;var z=(float)rock.At.Z;var radius=(float)rock.Height;
                var h=CoastalTerrain.Hash((int)x,(int)z);
                var at=P(x,z)+Vector3.up*(radius*.08f);
                var pink=Color.Lerp(new Color(.57f,.55f,.54f),new Color(.67f,.59f,.56f),(float)h);
                var kit=Tile(x,z);
                kit.Crown(at,new Vector3(radius,radius*.52f,radius*.7f),pink,1,(float)h*360);
                kit.Crown(at+new Vector3(radius*.45f,radius*.15f,radius*.25f),new Vector3(radius*.7f,radius*.38f,radius*.5f),
                    Color.Lerp(pink,new Color(.38f,.36f,.36f),.55f),0,(float)h*170);
            }
            foreach (var tile in tiles) tile.Value.Spawn("Coastal terrain and granite " + tile.Key, parent, material);
        }
    }
}

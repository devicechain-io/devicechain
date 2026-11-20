// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>Deterministic species and distance detail, merged by tile into one shared, shadow-free surface.</summary>
    internal sealed class TreeBuilder
    {
        readonly Dictionary<(int, int, bool), SceneryMesh> tiles = new Dictionary<(int, int, bool), SceneryMesh>();
        static readonly Color[] Autumn = { new Color(.32f,.39f,.19f), new Color(.65f,.53f,.23f),
            new Color(.76f,.40f,.15f), new Color(.57f,.24f,.13f), new Color(.45f,.44f,.20f) };

        public void Add(Vector3 at, float height, bool town)
        {
            var d = (float)CoastalTerrain.CoreDistance(at.x,at.z);
            var far = !town && d > 800;
            var tileSize = town ? 300 : 900;
            var key = (Mathf.FloorToInt(at.x/tileSize), Mathf.FloorToInt(at.z/tileSize), town);
            if (!tiles.TryGetValue(key, out var mesh)) tiles[key] = mesh = new SceneryMesh();
            var hash = (float)CoastalTerrain.Hash(Mathf.RoundToInt(at.x*7),Mathf.RoundToInt(at.z*7));
            var species = Mathf.Min(4, (int)(hash * 5)); // birch, maple, oak, spruce, pine
            var yaw = hash * 360;
            var lean = new Vector3(Mathf.Sin(yaw*Mathf.Deg2Rad)*.065f,0,Mathf.Cos(yaw*Mathf.Deg2Rad)*.045f) * height;
            var trunk = at + lean + Vector3.up * height * .54f;
            var bark = species == 0 ? new Color(.72f,.70f,.61f) : new Color(.27f,.22f,.17f);
            var radius = height * (.032f + hash*.012f);
            mesh.Branch(at-Vector3.up*.45f,trunk,radius,radius*.48f,bark,far ? 3 : 5);
            mesh.Branch(at-Vector3.up*.6f,at+Vector3.up*height*.10f,radius*1.65f,radius,
                new Color(.16f,.13f,.10f),far ? 3 : 5);
            var leaf = Autumn[(int)(CoastalTerrain.Hash((int)at.z, (int)at.x) * (Autumn.Length - .001))];
            leaf *= .91f + hash*.16f;
            if (species >= 3)
            {
                var green = new Color(.31f,.36f,.32f) * (.90f+hash*.20f);
                var tiers = far ? 3 : 5;
                for (var i=0;i<tiers;i++)
                {
                    var u = i/(float)tiers;
                    var basePoint = at + lean * (.3f+u*.7f) + Vector3.up * height * (.14f+u*.64f);
                    mesh.Branch(basePoint, at+lean+Vector3.up*height*(.56f+u*.55f), height*.33f*(1-u*.76f),0,
                        green*(.88f+u*.25f),far ? 5 : 7);
                }
                return;
            }
            var oval = species == 0 ? 1.12f : species == 2 ? .9f : 1;
            var crownR = height*(species == 2 ? .34f : .32f);
            var centerCrown = at+lean+Vector3.up*height*.64f;
            mesh.Crown(centerCrown,new Vector3(crownR,crownR*oval,crownR*.9f),leaf,far ? 0 : 1,yaw);
            var blobs = far ? 1 : 2;
            for (var i=0;i<blobs;i++)
            {
                var direction = Quaternion.Euler(0,yaw+i*155,0)*new Vector3(crownR*.65f,0,crownR*.20f);
                var end = centerCrown+direction-Vector3.up*height*(species==0 ? -.04f : .12f);
                if(!far)mesh.Branch(trunk-Vector3.up*height*.15f,end,radius*.55f,radius*.18f,bark,4);
                mesh.Crown(end,new Vector3(crownR*.69f,crownR*oval*.74f,crownR*.7f),
                    Color.Lerp(leaf,leaf*.62f,.65f),far || !town ? 0 : 1,yaw+i*47);
            }
            if (town && species == 0)
            {
                // Dark horizontal bark flecks, sparse enough to read on the pale birch trunk.
                for (var i=1;i<5;i++) mesh.Branch(at+(trunk-at)*(i/6f),at+(trunk-at)*(i/6f)+Vector3.up*.13f,
                    radius*1.03f,radius*1.03f,new Color(.30f,.29f,.25f),5);
            }
        }

        public void Flush(Transform town, Transform forest, Material material)
        {
            foreach (var tile in tiles) tile.Value.Spawn("Woodland stands " + tile.Key,tile.Key.Item3 ? town : forest,material,TownBuilder.FoliageLayer);
            tiles.Clear();
        }
    }
}

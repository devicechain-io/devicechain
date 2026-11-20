// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>A flat far-water patch in WORLD coordinates; empty patches collapse to a point.</summary>
    public readonly struct OceanPatch
    {
        public readonly double MinX, MaxX, MinZ, MaxZ;
        public bool Empty => MaxX <= MinX || MaxZ <= MinZ;
        public OceanPatch(double x0, double x1, double z0, double z1)
        { MinX=x0; MaxX=x1; MinZ=z0; MaxZ=z1; }
    }

    /// <summary>The camera-following square ring, intersected with the actual WORLD sea and harbour.
    /// There is no ocean north of the coast outside the mapped core, even beyond the terrain's far edge.</summary>
    public static class OceanRing
    {
        public const int PatchCount = 8;

        // The caller reuses these eight slots when the camera crosses a grid cell: no per-frame allocation.
        public static void Fill(double x, double z, double inner, double outer, OceanPatch[] patches)
        {
            if(patches == null || patches.Length < PatchCount) throw new ArgumentException("Eight patch slots required",nameof(patches));
            void Set(int i,double x0,double x1,double z0,double z1)
            {
                patches[i*2]=Clip(x0,x1,z0,Math.Min(z1,CoastalTerrain.CoastZ));
                patches[i*2+1]=Clip(Math.Max(x0,-CoastalTerrain.CoreHalfX),Math.Min(x1,CoastalTerrain.CoreHalfX),
                    Math.Max(z0,CoastalTerrain.CoastZ),Math.Min(z1,CoastalTerrain.CoreNorthZ));
            }
            Set(0,x-outer,x+outer,z+inner,z+outer);
            Set(1,x-outer,x+outer,z-outer,z-inner);
            Set(2,x-outer,x-inner,z-inner,z+inner);
            Set(3,x+inner,x+outer,z-inner,z+inner);
        }

        static OceanPatch Clip(double x0,double x1,double z0,double z1) =>
            x1>x0 && z1>z0 ? new OceanPatch(x0,x1,z0,z1) : default;
    }
}

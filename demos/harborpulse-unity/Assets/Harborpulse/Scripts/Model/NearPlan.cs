// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>The primitives a near-detail set is made of. Every one maps onto a flat-shaded SceneryMesh call.</summary>
    public enum NearShape : byte { Box, Beam, Cylinder, Gable, Quad }

    /// <summary>
    /// What a part is made of, as a role rather than a colour: the visual layer resolves each role against the building's own paint, roof,
    /// accent and hull. <see cref="Window"/> is the one role drawn with the shared window materials (so a near-only window follows the same
    /// night rule as every other window); everything else is vertex-coloured.
    /// </summary>
    public enum NearTone : byte
    {
        Paint, PaintDark, PaintLight, Trim, TrimDark, Stone, StoneDark, Brick, BrickLight, Mortar, Wood, WoodDark, WoodLight,
        Roof, RoofDark, Accent, AccentLight, Door, DoorPanel, Metal, MetalDark, Black, Glass, Foliage, FoliageDark,
        Red, Yellow, Orange, Cream, Rope, Hull, HullDark, Window,
    }

    public readonly struct NearVec
    {
        public readonly float X, Y, Z;
        public NearVec(float x, float y, float z) { X = x; Y = y; Z = z; }
    }

    /// <summary>One primitive in a building's (or vehicle's) local frame, origin on the ground at its centre, +Y up.</summary>
    public readonly struct NearPart
    {
        public readonly NearShape Shape;
        public readonly NearTone Tone;
        public readonly int Seed;
        /// <summary>Box: centre. Beam: start. Cylinder: base centre. Gable: eave centre. Quad: first corner.</summary>
        public readonly NearVec A;
        /// <summary>Box: size. Beam: end. Cylinder: top centre. Gable: (width, rise, depth). Quad: second corner.</summary>
        public readonly NearVec B;
        /// <summary>Quad: third corner. Beam: (depth, 0, 0). Cylinder: (top radius, segments, 0).</summary>
        public readonly NearVec C;
        public readonly NearVec D;
        /// <summary>Quad: the side that faces out.</summary>
        public readonly NearVec N;
        /// <summary>Beam width, cylinder radius.</summary>
        public readonly float R;
        public readonly float Yaw;
        public NearPart(NearShape shape, NearTone tone, int seed, NearVec a, NearVec b, NearVec c, NearVec d, NearVec n, float r, float yaw)
        { Shape = shape; Tone = tone; Seed = seed; A = a; B = b; C = c; D = d; N = n; R = r; Yaw = yaw; }

        /// <summary>The vertices SceneryMesh spends on this part (its quads are four, its triangles three, nothing shared).</summary>
        public int Vertices
        {
            get
            {
                switch (Shape)
                {
                    case NearShape.Box: case NearShape.Beam: return 24;
                    case NearShape.Gable: return 14;
                    case NearShape.Quad: return 4;
                    default: { var seg = (int)C.Y; return seg * (4 + (C.X > 0 ? 3 : 0) + 3); }
                }
            }
        }
    }

    /// <summary>
    /// A facade as a coordinate frame: <c>u</c> along it, <c>n</c> out from the wall plane, both axis-aligned in the building's frame. The
    /// four standard facades match the order the town's window rows were always written in (front is local -Z, the side the porch is on).
    /// </summary>
    public readonly struct NearFacade
    {
        public readonly float OX, OZ, AX, AZ, FX, FZ;
        NearFacade(float ox, float oz, float ax, float az, float fx, float fz) { OX = ox; OZ = oz; AX = ax; AZ = az; FX = fx; FZ = fz; }
        public static NearFacade Front(float w, float d) => new NearFacade(0, -d / 2, 1, 0, 0, -1);
        public static NearFacade Rear(float w, float d) => new NearFacade(0, d / 2, 1, 0, 0, 1);
        public static NearFacade Left(float w, float d) => new NearFacade(-w / 2, 0, 0, 1, -1, 0);
        public static NearFacade Right(float w, float d) => new NearFacade(w / 2, 0, 0, 1, 1, 0);
        public static NearFacade At(float ox, float oz, float ax, float az, float fx, float fz) => new NearFacade(ox, oz, ax, az, fx, fz);
        public float X(float u, float n) => OX + AX * u + FX * n;
        public float Z(float u, float n) => OZ + AZ * u + FZ * n;
        public float SizeX(float su, float sn) => Math.Abs(AX) * su + Math.Abs(FX) * sn;
        public float SizeZ(float su, float sn) => Math.Abs(AZ) * su + Math.Abs(FZ) * sn;
    }

    /// <summary>The window rows every near set aligns to: the same count and start the town's far windows use.</summary>
    public static class WindowRow
    {
        public static int Count(float span, float spacing) => Math.Max(1, (int)Math.Floor((span - 1.5f) / spacing));
        public static float Start(int count, float spacing) => -(count - 1) * spacing / 2;
    }

    /// <summary>A small deterministic hash of a position, for per-building choices that must not touch the shared seeded streams.</summary>
    public static class NearHash
    {
        public static float Unit(float x, float z, int salt)
        {
            unchecked
            {
                var ix = (int)Math.Round((double)x * 7); var iz = (int)Math.Round((double)z * 7);
                var h = (uint)(ix * 374761393 + iz * 668265263 + salt * 1442695041);
                h = (h ^ (h >> 13)) * 1274126177u;
                return ((h ^ (h >> 16)) & 0xffffff) / 16777215f;
            }
        }
    }

    /// <summary>How far past a building's walls its lot reserves ground (metres).</summary>
    public readonly struct NearReach
    {
        public readonly float Side, Front, Back;
        public NearReach(float side, float front, float back) { Side = side; Front = front; Back = back; }
        /// <summary>A town lot: the footprint is (w+1) by (d+7), so half a metre either side and three and a half front and back.</summary>
        public static readonly NearReach Lot = new NearReach(.5f, 3.5f, 3.5f);
    }

    /// <summary>
    /// A near-detail set as pure data: the primitives to add, in the local frame, on top of what the far level already draws. Keeping it here,
    /// not in the engine layer, is what lets the sidecar tests pin its bounds, vertex cost and determinism without Unity.
    /// </summary>
    public sealed class NearPlan
    {
        readonly List<NearPart> parts = new List<NearPart>();
        float yaw, cy = 1, sy;
        public string Name { get; }
        public NearPlan(string name) { Name = name; }
        public IReadOnlyList<NearPart> Parts => parts;
        public int Vertices { get; private set; }
        /// <summary>Whether any part is drawn with the window materials (the set then needs its second renderer).</summary>
        public bool HasWindows { get; private set; }

        /// <summary>Everything added after this is turned about the vertical by <paramref name="deg"/> (Unity's yaw), until the next call.</summary>
        public NearPlan Frame(float deg)
        {
            yaw = deg; var r = deg * (float)Math.PI / 180;
            cy = (float)Math.Cos(r); sy = (float)Math.Sin(r);
            return this;
        }

        NearVec V(float x, float y, float z) => new NearVec(x * cy + z * sy, y, -x * sy + z * cy);

        NearPlan Add(NearPart p) { parts.Add(p); Vertices += p.Vertices; if (p.Tone == NearTone.Window) HasWindows = true; return this; }

        public NearPlan Box(NearTone t, float cx, float cyy, float cz, float sx, float sh, float sz, int seed = 0)
            => Add(new NearPart(NearShape.Box, t, seed, V(cx, cyy, cz), new NearVec(sx, sh, sz), default, default, default, 0, yaw));

        public NearPlan Beam(NearTone t, float ax, float ay, float az, float bx, float by, float bz, float w, float depth = -1)
            => Add(new NearPart(NearShape.Beam, t, 0, V(ax, ay, az), V(bx, by, bz), new NearVec(depth < 0 ? w : depth, 0, 0), default, default, w, 0));

        public NearPlan Cyl(NearTone t, float bx, float by, float bz, float tx, float ty, float tz, float r, float topR = -1, int seg = 8)
            => Add(new NearPart(NearShape.Cylinder, t, 0, V(bx, by, bz), V(tx, ty, tz), new NearVec(topR < 0 ? r : topR, seg, 0), default, default, r, 0));

        /// <summary>An upright cylinder from <paramref name="y0"/> up by <paramref name="h"/>.</summary>
        public NearPlan Drum(NearTone t, float x, float y0, float z, float r, float h, int seg = 8, float topR = -1)
            => Cyl(t, x, y0, z, x, y0 + h, z, r, topR, seg);

        /// <summary>A gabled prism over a footprint centred on the eave, ridge along its local X (see MeshKit.Gable).</summary>
        public NearPlan Gable(NearTone t, float cx, float cyy, float cz, float width, float depth, float rise, float ownYaw = 0)
            => Add(new NearPart(NearShape.Gable, t, 0, V(cx, cyy, cz), new NearVec(width, rise, depth), default, default, default, 0, yaw + ownYaw));

        public NearPlan Quad(NearTone t, NearVec a, NearVec b, NearVec c, NearVec d, NearVec outward)
        {
            return Add(new NearPart(NearShape.Quad, t, 0, V(a.X, a.Y, a.Z), V(b.X, b.Y, b.Z), V(c.X, c.Y, c.Z), V(d.X, d.Y, d.Z), V(outward.X, outward.Y, outward.Z), 0, 0));
        }

        /// <summary>A box in a facade's frame: <c>u</c> along, <c>v</c> up, <c>n</c> out of the wall plane.</summary>
        public NearPlan Fb(NearFacade f, NearTone t, float u, float v, float n, float su, float sv, float sn, int seed = 0)
            => Box(t, f.X(u, n), v, f.Z(u, n), f.SizeX(su, sn), sv, f.SizeZ(su, sn), seed);

        /// <summary>A flat strip on a facade, just proud of the wall: a clapboard line, a batten, a brick course.</summary>
        public NearPlan Fq(NearFacade f, NearTone t, float u0, float v0, float u1, float v1, float n)
            => Quad(t, new NearVec(f.X(u0, n), v0, f.Z(u0, n)), new NearVec(f.X(u0, n), v1, f.Z(u0, n)), new NearVec(f.X(u1, n), v1, f.Z(u1, n)),
                new NearVec(f.X(u1, n), v0, f.Z(u1, n)), new NearVec(f.FX, 0, f.FZ));

        public NearPlan Fbeam(NearFacade f, NearTone t, float u0, float v0, float n0, float u1, float v1, float n1, float w, float depth = -1)
            => Beam(t, f.X(u0, n0), v0, f.Z(u0, n0), f.X(u1, n1), v1, f.Z(u1, n1), w, depth);

        public NearPlan Fdrum(NearFacade f, NearTone t, float u, float v0, float n, float r, float h, int seg = 8, float topR = -1)
            => Drum(t, f.X(u, n), v0, f.Z(u, n), r, h, seg, topR);

        // ---------------------------------------------------------------- measures

        /// <summary>The axis-aligned extent of every part, in the local frame (conservative for beams and cylinders).</summary>
        public void Bounds(out float minX, out float maxX, out float minY, out float maxY, out float minZ, out float maxZ)
        {
            float x0 = float.MaxValue, y0 = float.MaxValue, z0 = float.MaxValue, x1 = float.MinValue, y1 = float.MinValue, z1 = float.MinValue;
            void Pt(float x, float y, float z, float pad) => Pt3(x, y, z, pad, pad, pad);
            void Pt3(float x, float y, float z, float padX, float padY, float padZ)
            {
                x0 = Math.Min(x0, x - padX); x1 = Math.Max(x1, x + padX);
                y0 = Math.Min(y0, y - padY); y1 = Math.Max(y1, y + padY);
                z0 = Math.Min(z0, z - padZ); z1 = Math.Max(z1, z + padZ);
            }
            void Rect(NearVec c, float hx, float hy, float hz, float deg)
            {
                var r = deg * (float)Math.PI / 180; var co = (float)Math.Cos(r); var si = (float)Math.Sin(r);
                for (var sx = -1; sx <= 1; sx += 2) for (var sz = -1; sz <= 1; sz += 2) for (var sh = -1; sh <= 1; sh += 2)
                {
                    float x = sx * hx, z = sz * hz;
                    Pt(c.X + x * co + z * si, c.Y + sh * hy, c.Z - x * si + z * co, 0);
                }
            }
            void BeamCorners(NearPart b, Action<float, float, float, float> pt)
            {
                // the same cross-section SceneryMesh.Beam builds: width across, depth the other way, both square to the axis
                float ax = b.B.X - b.A.X, ay = b.B.Y - b.A.Y, az = b.B.Z - b.A.Z;
                var len = (float)Math.Sqrt(ax * ax + ay * ay + az * az);
                if (len < 1e-6f) return;
                ax /= len; ay /= len; az /= len;
                // reference: up unless the beam is nearly vertical, then forward
                float rx = 0, ry = 1, rz = 0;
                if (Math.Abs(ay) >= .95f) { ry = 0; rz = 1; }
                float ux = ay * rz - az * ry, uy = az * rx - ax * rz, uz = ax * ry - ay * rx;
                var ul = (float)Math.Sqrt(ux * ux + uy * uy + uz * uz); ux /= ul; uy /= ul; uz /= ul;
                float vx = ay * uz - az * uy, vy = az * ux - ax * uz, vz = ax * uy - ay * ux;
                for (var i = -1; i <= 1; i += 2) for (var j = -1; j <= 1; j += 2)
                {
                    float ox = ux * i * b.R * .5f + vx * j * b.C.X * .5f, oy = uy * i * b.R * .5f + vy * j * b.C.X * .5f, oz = uz * i * b.R * .5f + vz * j * b.C.X * .5f;
                    pt(b.A.X + ox, b.A.Y + oy, b.A.Z + oz, 0); pt(b.B.X + ox, b.B.Y + oy, b.B.Z + oz, 0);
                }
            }
            foreach (var p in parts)
            {
                switch (p.Shape)
                {
                    case NearShape.Box: Rect(p.A, p.B.X / 2, p.B.Y / 2, p.B.Z / 2, p.Yaw); break;
                    case NearShape.Gable: Rect(new NearVec(p.A.X, p.A.Y + p.B.Y / 2, p.A.Z), p.B.X / 2, p.B.Y / 2, p.B.Z / 2, p.Yaw); break;
                    case NearShape.Beam: BeamCorners(p, Pt); break;
                    case NearShape.Cylinder:
                        {
                            // the ring's extent along each axis is the radius times the sine of the angle between that axis and the cylinder's
                            float ax = p.B.X - p.A.X, ay = p.B.Y - p.A.Y, az = p.B.Z - p.A.Z;
                            var len = (float)Math.Sqrt(ax * ax + ay * ay + az * az);
                            if (len > 1e-6f) { ax /= len; ay /= len; az /= len; }
                            var r = Math.Max(p.R, p.C.X);
                            float px = r * (float)Math.Sqrt(Math.Max(0, 1 - ax * ax)), py = r * (float)Math.Sqrt(Math.Max(0, 1 - ay * ay)), pz = r * (float)Math.Sqrt(Math.Max(0, 1 - az * az));
                            Pt3(p.A.X, p.A.Y, p.A.Z, px, py, pz); Pt3(p.B.X, p.B.Y, p.B.Z, px, py, pz);
                            break;
                        }
                    default: Pt(p.A.X, p.A.Y, p.A.Z, 0); Pt(p.B.X, p.B.Y, p.B.Z, 0); Pt(p.C.X, p.C.Y, p.C.Z, 0); Pt(p.D.X, p.D.Y, p.D.Z, 0); break;
                }
            }
            if (parts.Count == 0) { x0 = x1 = y0 = y1 = z0 = z1 = 0; }
            minX = x0; maxX = x1; minY = y0; maxY = y1; minZ = z0; maxZ = z1;
        }

        /// <summary>The set's bounding sphere in the local frame: the centre of its extent and half its diagonal, for the range test.</summary>
        public void Sphere(out float x, out float y, out float z, out float radius)
        {
            Bounds(out var x0, out var x1, out var y0, out var y1, out var z0, out var z1);
            x = (x0 + x1) / 2; y = (y0 + y1) / 2; z = (z0 + z1) / 2;
            float dx = x1 - x0, dy = y1 - y0, dz = z1 - z0;
            radius = (float)Math.Sqrt(dx * dx + dy * dy + dz * dz) / 2;
        }
    }
}

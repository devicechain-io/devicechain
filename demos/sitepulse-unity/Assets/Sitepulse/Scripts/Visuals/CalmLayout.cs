// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Sitepulse.Domain;
using UnityEngine;

namespace DeviceChain.Sitepulse.Visuals
{
    /// <summary>The glide every moving box shares with the cards: a critically damped ease, no faster than <see cref="CardTiming.GlideMaxSpeed"/>; reduced motion (and a still) snap.</summary>
    public static class CalmGlide
    {
        public static float Step(float current, float target, ref float velocity, float dt, bool reduced, bool snap)
        {
            if (snap || reduced)
            {
                velocity = 0f;
                return target;
            }

            if (dt <= 0f) return current;
            return Mathf.SmoothDamp(current, target, ref velocity, CardTiming.GlideSeconds, CardTiming.GlideMaxSpeed, dt);
        }

        public static Vector2 Step(Vector2 current, Vector2 target, ref Vector2 velocity, float dt, bool reduced, bool snap)
        {
            if (snap || reduced)
            {
                velocity = Vector2.zero;
                return target;
            }

            if (dt <= 0f) return current;
            return Vector2.SmoothDamp(current, target, ref velocity, CardTiming.GlideSeconds, CardTiming.GlideMaxSpeed, dt);
        }
    }

    /// <summary>
    /// Where the proof drawer stands. Its TOP edge is pinned: the place is worked out from the height of a full drawer, so a row more
    /// grows the box downward and nothing already on screen moves.
    /// </summary>
    public static class DrawerLayout
    {
        public const float W = 760f, RowH = 25f, HeadH = 52f, Pad = 14f;
        public const int MaxRows = 12;

        /// <summary>The drawer's height, in its own units, with <paramref name="rows"/> rows (an empty one has the room for the sentence that says so).</summary>
        public static float HeightFor(int rows) => HeadH + Pad + Math.Max(1, rows) * RowH + Pad * 0.5f;

        public static float FullHeight => HeightFor(MaxRows);

        /// <summary>The scale does not depend on the rows held, so a row more does not resize the drawer.</summary>
        public static float ScaleFor(DrawerMode mode, float refW) =>
            mode == DrawerMode.Full
                ? Mathf.Min(1.9f, (refW * 0.86f) / W, (OverlayStyle.RefH * 0.8f) / FullHeight)
                : Mathf.Min(1f, (refW - 2f * OverlayStyle.Margin) / W);

        /// <summary>The drawer's rectangle (layout units, origin lower left) when its box is <paramref name="height"/> tall (its own units).</summary>
        public static Rect Place(DrawerMode mode, float refW, float height)
        {
            var scale = ScaleFor(mode, refW);
            var size = new Vector2(W, height) * scale;
            var portrait = refW < OverlayStyle.RefH;
            var fullScaled = FullHeight * scale;
            float x, top;
            if (mode == DrawerMode.Full)
            {
                x = (refW - size.x) / 2f;
                top = (OverlayStyle.RefH + fullScaled) / 2f;
            }
            else if (portrait)
            {
                x = (refW - size.x) / 2f;
                top = OverlayStyle.RefH * 0.20f + OverlayStyle.Margin + fullScaled;
            }
            else
            {
                x = refW - OverlayStyle.Margin - size.x;
                top = (OverlayStyle.RefH + fullScaled) / 2f;
            }

            return new Rect(x, top - size.y, size.x, size.y);
        }
    }

    /// <summary>Where a stack of chips stands: from a corner, each after the one before, whatever is showing.</summary>
    public static class ChipStack
    {
        /// <summary>Fills <paramref name="into"/> with the lower-left corner of every chip; one that is not <paramref name="on"/> takes no room.</summary>
        public static void Place(IReadOnlyList<Vector2> sizes, IReadOnlyList<bool> on, bool portrait, float x, float startY, float gap, List<Vector2> into)
        {
            into.Clear();
            var y = startY;
            for (var i = 0; i < sizes.Count; i++)
            {
                if (!on[i])
                {
                    into.Add(new Vector2(x, y));
                    continue;
                }

                if (portrait) y -= sizes[i].y;
                into.Add(new Vector2(x, y));
                y += portrait ? -gap : sizes[i].y + gap;
            }
        }
    }
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Sitepulse.Visuals;
using NUnit.Framework;
using UnityEngine;

namespace DeviceChain.Sitepulse.Tests
{
    // The geofence label follows the cards' calm rules: it keeps its fence point through a short blockage,
    // moves after HoldSeconds, rides its point exactly while the camera moves, and glides between points.
    public sealed class LabelPlacerTests
    {
        const float Dt = 1f / 60f;

        static (Vector2, bool) Place(int i, bool[] valid, Vector2 shift = default) =>
            (new Vector2(100f + 300f * i, 200f) + shift, valid[i]);

        static LabelPlacer Settled(bool[] valid)
        {
            var l = new LabelPlacer();
            for (int f = 0; f < 60; f++) l.Step(valid.Length, i => Place(i, valid), Dt, false);
            return l;
        }

        [Test]
        public void ItTakesTheFirstClearPointAndFadesIn()
        {
            var l = new LabelPlacer();
            l.Step(3, i => Place(i, new[] { false, true, true }), Dt, false);
            Assert.AreEqual(1, l.Index);
            Assert.AreEqual(new Vector2(400f, 200f), l.Position, "the first placement appears where it belongs");
            Assert.Less(l.Alpha, 1f, "and fades in rather than popping");
            for (int f = 0; f < 30; f++) l.Step(3, i => Place(i, new[] { false, true, true }), Dt, false);
            Assert.AreEqual(1f, l.Alpha);
        }

        [Test]
        public void AShortBlockageDoesNotMoveIt()
        {
            var valid = new[] { true, true };
            var l = Settled(valid);
            valid[0] = false;                                   // a truck drives across the label's place
            for (int f = 0; f < 18; f++) l.Step(2, i => Place(i, valid), Dt, false);   // 0.3 s
            valid[0] = true;
            l.Step(2, i => Place(i, valid), Dt, false);
            Assert.AreEqual(0, l.Index, "0.3 s of blockage keeps the point");
        }

        [Test]
        public void ALongBlockageMovesItAndTheMoveGlides()
        {
            var valid = new[] { true, true };
            var l = Settled(valid);
            valid[0] = false;
            var before = l.Position;
            float largest = 0f;
            for (int f = 0; f < 90; f++)                        // 1.5 s
            {
                var was = l.Position;
                l.Step(2, i => Place(i, valid), Dt, false);
                largest = Mathf.Max(largest, (l.Position - was).magnitude);
            }
            Assert.AreEqual(1, l.Index, "blocked longer than the hold: a new point");
            Assert.AreEqual(Place(1, valid).Item1.x, l.Position.x, 1f, "and it arrives there");
            Assert.Less(largest, (Place(1, valid).Item1 - before).magnitude / 3f, "never in one jump");
        }

        [Test]
        public void ItRidesItsPointExactlyWhileTheCameraMoves()
        {
            var valid = new[] { true, true };
            var l = Settled(valid);
            for (int f = 1; f <= 30; f++)
            {
                var shift = new Vector2(f * 7f, -f * 3f);       // the fence point moves on screen every frame
                l.Step(2, i => Place(i, valid, shift), Dt, false);
                Assert.AreEqual(Place(0, valid, shift).Item1, l.Position, "no lag behind a point that did not change");
            }
        }

        [Test]
        public void ReducedMotionMovesWithoutAGlide()
        {
            var valid = new[] { true, true };
            var l = new LabelPlacer();
            for (int f = 0; f < 60; f++) l.Step(2, i => Place(i, valid), Dt, true);
            valid[0] = false;
            for (int f = 0; f < 60; f++) l.Step(2, i => Place(i, valid), Dt, true);
            Assert.AreEqual(Place(1, valid).Item1, l.Position);
        }

        [Test]
        public void NoClearPointHidesItByFading()
        {
            var valid = new[] { true };
            var l = Settled(valid);
            valid[0] = false;
            for (int f = 0; f < 120; f++) l.Step(1, i => Place(i, valid), Dt, false);
            Assert.AreEqual(0f, l.Alpha);
            Assert.AreEqual(-1, l.Index);
        }
    }
}

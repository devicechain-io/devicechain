// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Sitepulse.Visuals;
using NUnit.Framework;
using UnityEngine;

namespace DeviceChain.Sitepulse.Tests
{
    /// <summary>A card is always drawn inside the frame's safe area, and a card that is to be shown always has a slot.</summary>
    public sealed class CardSafeAreaTests
    {
        static readonly Rect Wide = CardSafeArea.Of(1920f, 1080f);
        static readonly Rect Tall = CardSafeArea.Of(607.5f, 1080f, 0.14f, 0.20f);
        static readonly Vector2 Card = new Vector2(252f, 200f);

        [Test]
        public void TheSafeAreaIsAboutTwoPercentInFromEveryEdge()
        {
            Assert.AreEqual(21.6f, Wide.xMin, 0.01f);
            Assert.AreEqual(21.6f, Wide.yMin, 0.01f);
            Assert.AreEqual(1920f - 21.6f, Wide.xMax, 0.01f);
            Assert.AreEqual(1080f - 21.6f, Wide.yMax, 0.01f);
        }

        [Test]
        public void ThePortraitBandsAreHeldClear()
        {
            Assert.AreEqual(1080f * 0.20f, Tall.yMin, 0.01f);
            Assert.AreEqual(1080f * 0.86f, Tall.yMax, 0.01f);
        }

        [TestCase(0f, 1070f, Description = "header past the top edge")]
        [TestCase(0f, 5f, Description = "foot past the bottom edge")]
        [TestCase(5f, 400f, Description = "past the left edge")]
        [TestCase(1900f, 400f, Description = "past the right edge")]
        public void ASlotCrossingTheSafeEdgeIsNotInside(float x, float y)
        {
            if (x == 0f) x = 800f;
            Assert.IsFalse(CardSafeArea.Inside(new Rect(x, y, Card.x, Card.y), Wide));
        }

        [Test]
        public void ASlotWellInsideIsInside() =>
            Assert.IsTrue(CardSafeArea.Inside(new Rect(800f, 400f, Card.x, Card.y), Wide));

        [Test]
        public void TheClampKeepsTheRectInsideFromAnyPlace()
        {
            for (float y = -400f; y < 1500f; y += 37f)
            for (float x = -400f; x < 2400f; x += 53f)
            {
                var r = new Rect(x, y, Card.x, Card.y);
                r.position += CardSafeArea.Shift(r, Wide);
                Assert.IsTrue(CardSafeArea.Inside(r, Wide), $"({x},{y}) -> {r}");
            }
        }

        [Test]
        public void ARectAlreadyInsideIsNotMoved() =>
            Assert.AreEqual(Vector2.zero, CardSafeArea.Shift(new Rect(800f, 400f, Card.x, Card.y), Wide));

        [Test]
        public void ARectTooBigForTheAreaIsCentredOnIt()
        {
            var r = new Rect(0f, 0f, 700f, 200f);
            r.position += CardSafeArea.Shift(r, Tall);
            Assert.AreEqual(Tall.center.x, r.center.x, 0.01f);
        }

        [Test]
        public void TheClampMovesNoFasterThanTheCardItClamps()
        {
            // a card riding a camera that carries it up through the top edge and on: the drawn place never jumps
            var prev = default(Vector2);
            bool first = true;
            for (float y = 700f; y < 1400f; y += 4f)
            {
                var r = new Rect(900f, y, Card.x, Card.y);
                var drawn = r.position + CardSafeArea.Shift(r, Wide);
                if (!first) Assert.LessOrEqual((drawn - prev).magnitude, 4f + 0.001f, $"jump at y={y}");
                prev = drawn;
                first = false;
            }
        }

        [Test]
        public void TheClampStaysWithinTheGlideSpeedLimit()
        {
            // the glide of a slot (SmoothDamp, capped at GlideMaxSpeed) and then the clamp: the clamp adds no speed
            var slot = new CardSlot();
            slot.Assign(new Vector2(0f, 0f), Vector2.zero, false);
            slot.Step(0.016f, true, false, false);
            slot.Assign(new Vector2(0f, 900f), new Vector2(0f, 900f), false);
            var anchor = new Vector2(900f, 300f);
            var last = default(Vector2);
            bool first = true;
            for (int i = 0; i < 120; i++)
            {
                slot.Step(0.016f, true, false, false);
                var r = new Rect(anchor + slot.Display, Card);
                var drawn = r.position + CardSafeArea.Shift(r, Wide);
                if (!first) Assert.LessOrEqual((drawn - last).magnitude / 0.016f, CardTiming.GlideMaxSpeed + 0.5f, $"frame {i}");
                last = drawn;
                first = false;
            }
        }

        [Test]
        public void ABestSlotThatIsValidIsTakenAsItIs()
        {
            CardSlotSearch.Find(new Vector2(960f, 500f), Card, Wide, (r, e) => true, r => 0f, (r, e) => 0f, out var rect, out _, out bool exact, out _);
            Assert.IsTrue(exact);
            Assert.IsTrue(CardSafeArea.Inside(rect, Wide));
        }

        [Test]
        public void ACardWithNoValidSlotStillGetsOneInsideTheSafeArea()
        {
            // nothing is valid, as in a narrow frame crowded with blockers
            var anchor = new Vector2(300f, 600f);
            CardSlotSearch.Find(anchor, Card, Tall, (r, e) => false, r => 0f, (r, e) => 0f, out var rect, out var elbow, out bool exact, out float cost);
            Assert.IsFalse(exact);
            Assert.IsFalse(float.IsPositiveInfinity(cost));
            Assert.IsTrue(CardSafeArea.Inside(rect, Tall), rect.ToString());
            Assert.AreEqual(0f, (CardSafeArea.Nearest(rect, elbow) - elbow).magnitude, 0.01f, "the leader bends on the card");
        }

        [Test]
        public void TheLeastBadSlotIsTheOneThatCoversTheLeast()
        {
            // the whole left half is something a card must not cover; the right is free but still "invalid" by the leader fault
            var wall = new Rect(0f, 0f, 304f, 1080f);
            var anchor = new Vector2(400f, 540f);
            CardSlotSearch.Find(anchor, Card, Tall, (r, e) => false, r => 0f, (r, e) => CardSafeArea.OverlapArea(r, wall), out var rect, out _, out bool exact, out _);
            Assert.IsFalse(exact);
            Assert.AreEqual(0f, CardSafeArea.OverlapArea(rect, wall), 0.01f);
        }

        [Test]
        public void ASlotOverTheTopEdgeIsNeverChosen()
        {
            // an anchor at the top of the frame: every slot above it crosses the edge, so none is chosen
            CardSlotSearch.Find(new Vector2(960f, 1060f), Card, Wide, (r, e) => true, r => 0f, (r, e) => 0f, out var rect, out _, out bool exact, out _);
            Assert.IsTrue(exact);
            Assert.LessOrEqual(rect.yMax, Wide.yMax);
        }
    }
}

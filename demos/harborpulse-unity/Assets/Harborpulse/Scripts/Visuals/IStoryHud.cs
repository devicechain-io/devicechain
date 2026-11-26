// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Demos.Hud;
using DeviceChain.Harborpulse.Model.Director;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>Everything the story HUD draws in one frame. The composition root fills it; the HUD decides nothing.</summary>
    public sealed class HudFrame
    {
        public DirectorView View;
        /// <summary>What kind of picture this is: RECORDED, LIVE, OFFLINE PREVIEW or TEST CONTROL.</summary>
        public string Badge;
        /// <summary>The scene's clock, in sim hours.</summary>
        public double Sim;
        /// <summary>
        /// The presentation clock every fade runs on: a rendered cut's show seconds (so a frame is the same whatever the machine's
        /// speed), the director's seconds live.
        /// </summary>
        public double ClockS;
        /// <summary>The devices with an active alarm (one pin each), and the names written in the world (a shot's subjects only).</summary>
        public IReadOnlyList<PinInput> Pins = Array.Empty<PinInput>();
        public IReadOnlyList<WorldTag> Tags = Array.Empty<WorldTag>();
        public string ExtraProblem;
        /// <summary>The strip's rate chip where the view has none of its own (the offline preview's speed); null leaves the view's.</summary>
        public string RateChip;
    }

    /// <summary>ugui is supplied by the Hud assembly; portable visuals need only this interface.</summary>
    public interface IStoryHud : IDisposable
    {
        bool Visible { get; set; }
        void Apply(HudFrame frame);
        /// <summary>A new shot begins: the caption goes with the old one at once, and alarms already raised are drawn without a raise.</summary>
        void Cut();
        /// <summary>Every string the HUD is drawing now, for a render's text audit.</summary>
        IEnumerable<string> DrawnText { get; }
        /// <summary>
        /// The panels on screen now, each as its name, its rectangle in screen pixels (x, y, width, height from the bottom left) and
        /// its opacity 0 .. 1: a render samples the frame's pixels inside each to prove nothing of the world was drawn over it.
        /// </summary>
        IReadOnlyList<KeyValuePair<string, float[]>> Panels { get; }
        /// <summary>How many world markers (pins, ticks, names) the panels hid this frame.</summary>
        int MarkersHidden { get; }
    }
}

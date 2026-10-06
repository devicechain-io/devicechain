// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Sitepulse.Replay;

namespace DeviceChain.Sitepulse.App
{
    /// <summary>The interactive replay's screen: the app's HUD, which the replay root (in an assembly that has no HUD) speaks through.</summary>
    public sealed class HudReplayScreen : IReplayScreen
    {
        readonly SitepulseHud hud;

        public HudReplayScreen(SitepulseHud hud)
        {
            this.hud = hud ?? throw new ArgumentNullException(nameof(hud));
        }

        public void SetBadge(string text, bool error) => hud.SetBadge(text, error ? BadgeTone.Error : BadgeTone.Replay);

        public void ShowTimeline(string text) => hud.ShowTasks(text != null ? HudText.Esc(text) : null);

        public void ShowHelp(string text) => hud.ShowHelp(text);

        public void ShowError(string title, string message) => hud.ShowError(title, message);
    }
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

namespace DeviceChain.Sitepulse.Replay
{
    /// <summary>
    /// What a replay says on screen. The replay root knows only this: the screen-space HUD lives in the App assembly (which also holds
    /// the Live composition and so references the platform), and an adapter there implements it. An offline render is given no screen at all.
    /// </summary>
    public interface IReplayScreen
    {
        /// <summary>The mode badge; <paramref name="error"/> draws it as a refusal.</summary>
        void SetBadge(string text, bool error);

        /// <summary>The timeline panel (plain text); null hides it.</summary>
        void ShowTimeline(string text);

        /// <summary>The key help line; null hides it.</summary>
        void ShowHelp(string text);

        /// <summary>What could not start, and why.</summary>
        void ShowError(string title, string message);
    }
}

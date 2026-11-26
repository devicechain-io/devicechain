// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Demos.Hud
{
    /// <summary>Who is speaking in a caption: the scenario (a grey rule over the box) or the platform's record (a blue one).</summary>
    public enum CaptionTone { Scenario, Platform }

    /// <summary>What a caption box holds: a kicker in small capitals (place and subject), the caption (at most two lines) and small print under it.</summary>
    public sealed class CaptionContent
    {
        public static readonly CaptionContent Empty = new CaptionContent("", "", "", CaptionTone.Scenario);
        public CaptionContent(string kicker, string text, string note, CaptionTone tone)
        {
            Kicker = kicker ?? ""; Text = CaptionBoxState.TwoLines(text); Note = note ?? ""; Tone = tone;
        }
        public string Kicker { get; }
        public string Text { get; }
        public string Note { get; }
        public CaptionTone Tone { get; }
        public bool IsEmpty => Text.Length == 0 && Note.Length == 0;
        internal bool SameWords(CaptionContent other) => Text == other.Text && Note == other.Note;
    }

    /// <summary>
    /// The caption box's fade. A caption never swaps its words in place: the box fades out, stays away for a moment and fades back in
    /// with the new line. The clock is the caller's (a show clock for a rendered cut, a wall clock live), and the state only moves
    /// forward with it, so the same clock gives the same frames.
    /// </summary>
    public sealed class CaptionBoxState
    {
        public const double FadeOutS = 0.25, GapS = 0.15, FadeInS = 0.25;
        public const int MaxLines = 2;

        CaptionContent shown = CaptionContent.Empty, pending;
        double fadeInFromS = double.NegativeInfinity, fadeOutFromS = double.NaN;

        /// <summary>What is drawn now (the outgoing caption until its fade has finished).</summary>
        public CaptionContent Shown => shown;

        /// <summary>0 .. 1: how much of the box is there.</summary>
        public float Alpha { get; private set; }

        /// <summary>A caption is at most two lines: anything after the second is dropped rather than drawn off the box.</summary>
        public static string TwoLines(string text)
        {
            if (string.IsNullOrEmpty(text)) return "";
            var lines = text.Replace("\r", "").Split('\n');
            return lines.Length <= MaxLines ? string.Join("\n", lines) : lines[0] + "\n" + lines[1];
        }

        /// <summary>Asks for a caption (or, with empty words, for none). The same words again change nothing but the kicker and the tone.</summary>
        public void Set(CaptionContent content, double nowS)
        {
            content = content ?? CaptionContent.Empty;
            var target = pending ?? shown;
            if (target.SameWords(content))
            {
                // a kicker (a chapter tag taking its place for a moment) changes in place: the caption's words are not re-faded for it
                if (pending != null) pending = content; else shown = content;
                Tick(nowS); return;
            }
            if (shown.IsEmpty && pending == null) { shown = content; fadeInFromS = nowS; fadeOutFromS = double.NaN; }
            else
            {
                pending = content;
                if (double.IsNaN(fadeOutFromS)) fadeOutFromS = nowS;
            }
            Tick(nowS);
        }

        /// <summary>A hard cut: what was on the box goes with the shot, at once. The next caption fades in.</summary>
        public void Cut()
        {
            shown = CaptionContent.Empty; pending = null; fadeOutFromS = double.NaN; fadeInFromS = double.NegativeInfinity; Alpha = 0;
        }

        public void Tick(double nowS)
        {
            if (!double.IsNaN(fadeOutFromS))
            {
                var since = nowS - fadeOutFromS;
                if (since >= FadeOutS + GapS)
                {
                    fadeInFromS = fadeOutFromS + FadeOutS + GapS;
                    shown = pending ?? CaptionContent.Empty; pending = null; fadeOutFromS = double.NaN;
                }
                else { Alpha = (float)Math.Max(0, 1 - since / FadeOutS); return; }
            }
            Alpha = shown.IsEmpty ? 0 : (float)Math.Max(0, Math.Min(1, (nowS - fadeInFromS) / FadeInS));
        }
    }
}

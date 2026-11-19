// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using UnityEngine;

namespace DeviceChain.Harborpulse.EditorTools
{
    /// <summary>
    /// Burns a label into a still, so an image of the offline preview carries its own "this is not platform data" wherever it is copied: the
    /// scene's banner is drawn by IMGUI, which a camera render does not capture. A 5x7 block font, upper case, digits and a little punctuation.
    /// </summary>
    public static class PreviewStamp
    {
        static readonly Dictionary<char, string[]> Glyphs = new Dictionary<char, string[]>
        {
            { 'A', new[] { "01110", "10001", "10001", "11111", "10001", "10001", "10001" } },
            { 'B', new[] { "11110", "10001", "10001", "11110", "10001", "10001", "11110" } },
            { 'C', new[] { "01110", "10001", "10000", "10000", "10000", "10001", "01110" } },
            { 'D', new[] { "11110", "10001", "10001", "10001", "10001", "10001", "11110" } },
            { 'E', new[] { "11111", "10000", "10000", "11110", "10000", "10000", "11111" } },
            { 'F', new[] { "11111", "10000", "10000", "11110", "10000", "10000", "10000" } },
            { 'G', new[] { "01110", "10001", "10000", "10111", "10001", "10001", "01111" } },
            { 'H', new[] { "10001", "10001", "10001", "11111", "10001", "10001", "10001" } },
            { 'I', new[] { "01110", "00100", "00100", "00100", "00100", "00100", "01110" } },
            { 'J', new[] { "00111", "00010", "00010", "00010", "00010", "10010", "01100" } },
            { 'K', new[] { "10001", "10010", "10100", "11000", "10100", "10010", "10001" } },
            { 'L', new[] { "10000", "10000", "10000", "10000", "10000", "10000", "11111" } },
            { 'M', new[] { "10001", "11011", "10101", "10101", "10001", "10001", "10001" } },
            { 'N', new[] { "10001", "11001", "10101", "10011", "10001", "10001", "10001" } },
            { 'O', new[] { "01110", "10001", "10001", "10001", "10001", "10001", "01110" } },
            { 'P', new[] { "11110", "10001", "10001", "11110", "10000", "10000", "10000" } },
            { 'Q', new[] { "01110", "10001", "10001", "10001", "10101", "10010", "01101" } },
            { 'R', new[] { "11110", "10001", "10001", "11110", "10100", "10010", "10001" } },
            { 'S', new[] { "01111", "10000", "10000", "01110", "00001", "00001", "11110" } },
            { 'T', new[] { "11111", "00100", "00100", "00100", "00100", "00100", "00100" } },
            { 'U', new[] { "10001", "10001", "10001", "10001", "10001", "10001", "01110" } },
            { 'V', new[] { "10001", "10001", "10001", "10001", "10001", "01010", "00100" } },
            { 'W', new[] { "10001", "10001", "10001", "10101", "10101", "10101", "01010" } },
            { 'X', new[] { "10001", "10001", "01010", "00100", "01010", "10001", "10001" } },
            { 'Y', new[] { "10001", "10001", "01010", "00100", "00100", "00100", "00100" } },
            { 'Z', new[] { "11111", "00001", "00010", "00100", "01000", "10000", "11111" } },
            { '0', new[] { "01110", "10001", "10011", "10101", "11001", "10001", "01110" } },
            { '1', new[] { "00100", "01100", "00100", "00100", "00100", "00100", "01110" } },
            { '2', new[] { "01110", "10001", "00001", "00010", "00100", "01000", "11111" } },
            { '3', new[] { "11110", "00001", "00001", "01110", "00001", "00001", "11110" } },
            { '4', new[] { "00010", "00110", "01010", "10010", "11111", "00010", "00010" } },
            { '5', new[] { "11111", "10000", "11110", "00001", "00001", "10001", "01110" } },
            { '6', new[] { "00110", "01000", "10000", "11110", "10001", "10001", "01110" } },
            { '7', new[] { "11111", "00001", "00010", "00100", "01000", "01000", "01000" } },
            { '8', new[] { "01110", "10001", "10001", "01110", "10001", "10001", "01110" } },
            { '9', new[] { "01110", "10001", "10001", "01111", "00001", "00010", "01100" } },
            { ':', new[] { "00000", "00100", "00100", "00000", "00100", "00100", "00000" } },
            { '-', new[] { "00000", "00000", "00000", "11111", "00000", "00000", "00000" } },
            { '.', new[] { "00000", "00000", "00000", "00000", "00000", "00100", "00100" } },
            { '_', new[] { "00000", "00000", "00000", "00000", "00000", "00000", "11111" } },
            { '/', new[] { "00001", "00010", "00010", "00100", "01000", "01000", "10000" } },
            { '%', new[] { "11001", "11010", "00010", "00100", "01000", "01011", "10011" } },
            { '(', new[] { "00010", "00100", "01000", "01000", "01000", "00100", "00010" } },
            { ')', new[] { "01000", "00100", "00010", "00010", "00010", "00100", "01000" } },
            { ',', new[] { "00000", "00000", "00000", "00000", "00100", "00100", "01000" } },
            { ' ', new[] { "00000", "00000", "00000", "00000", "00000", "00000", "00000" } },
        };

        /// <summary>Camera.Render does not draw IMGUI. Burn the same evidence card into the PNG.</summary>
        public static int Card(Texture2D tex, string text, int left, int top, int width)
        {
            var scale = tex.width >= 1000 ? 2 : 1;
            width = Mathf.Min(width, tex.width - 24); left = Mathf.Max(12, left);
            var columns = Mathf.Max(12, (width - 16) / (6 * scale));
            var rows = new List<string>();
            foreach (var line in text.Replace("\u00b7", "-").Replace("\u00b0", " DEG").Split('\n'))
            {
                var remaining = line;
                while (remaining.Length > columns)
                {
                    var cut = remaining.LastIndexOf(' ', columns);
                    if (cut < columns / 2) cut = columns;
                    rows.Add(remaining.Substring(0, cut)); remaining = remaining.Substring(cut).TrimStart();
                }
                rows.Add(remaining);
            }
            var height = rows.Count * 9 * scale + 12;
            if (top + height > tex.height - 12) return top; // never obscure the preview stamp or write outside the image
            for (var y = top; y < top + height; y++) for (var x = left; x < left + width; x++)
                tex.SetPixel(x, tex.height - 1 - y, new Color(0.035f, 0.055f, 0.08f));
            for (var row = 0; row < rows.Count; row++)
            {
                var pen = left + 8;
                foreach (var raw in rows[row].ToUpperInvariant())
                {
                    if (!Glyphs.TryGetValue(raw, out var glyph)) glyph = Glyphs[' '];
                    for (var y = 0; y < 7; y++) for (var x = 0; x < 5; x++) if (glyph[y][x] == '1')
                        for (var dy = 0; dy < scale; dy++) for (var dx = 0; dx < scale; dx++)
                            tex.SetPixel(pen + x * scale + dx, tex.height - 1 - (top + 6 + row * 9 * scale + y * scale + dy), new Color(0.86f, 0.91f, 0.95f));
                    pen += 6 * scale;
                }
            }
            tex.Apply(); return top + height + 8;
        }

        /// <summary>Draws <paramref name="text"/> in black on an amber bar across the top of <paramref name="tex"/>.</summary>
        public static void Stamp(Texture2D tex, string text, int scale = 3)
        {
            var bar = new Color(1.0f, 0.78f, 0.2f);
            int barHeight = 7 * scale + 2 * 4 * scale / 3 + 8;
            for (var y = tex.height - barHeight; y < tex.height; y++)
            for (var x = 0; x < tex.width; x++)
                tex.SetPixel(x, y, bar);
            var penX = 12;
            var top = tex.height - 6;
            foreach (var raw in text.ToUpperInvariant())
            {
                if (!Glyphs.TryGetValue(raw, out var g)) g = Glyphs[' '];
                for (var row = 0; row < 7; row++)
                for (var col = 0; col < 5; col++)
                {
                    if (g[row][col] != '1') continue;
                    for (var dy = 0; dy < scale; dy++)
                    for (var dx = 0; dx < scale; dx++)
                    {
                        var px = penX + col * scale + dx;
                        var py = top - row * scale - dy;
                        if (px < tex.width && py >= 0) tex.SetPixel(px, py, Color.black);
                    }
                }

                penX += 6 * scale;
            }

            tex.Apply();
        }
    }
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A declaration-only stand-in for uGUI (com.unity.ugui). It declares ONLY the members the demo code touches, so a
// demo that starts using another member fails the compile check with a missing-member error, and
// the stub is extended in the same change. Never shipped, never run: it exists so the sources can be
// compiled against real UnityEngine reference assemblies with no Editor or licence, and it proves
// nothing about behaviour.
using System.Collections.Generic;
using UnityEngine;

namespace UnityEngine.UI
{
    public abstract class Graphic : Behaviour
    {
        public Color color { get; set; }
        public bool raycastTarget { get; set; }
        public Material material { get; set; }
        public RectTransform rectTransform { get; }
    }

    public class Image : Graphic { public Sprite sprite { get; set; } }
    public class RawImage : Graphic { public Texture texture { get; set; } }

    public class Text : Graphic
    {
        public string text { get; set; }
        public int fontSize { get; set; }
        public FontStyle fontStyle { get; set; }
        public Font font { get; set; }
        public TextAnchor alignment { get; set; }
        public HorizontalWrapMode horizontalOverflow { get; set; }
        public VerticalWrapMode verticalOverflow { get; set; }
        public bool supportRichText { get; set; }
        public float preferredWidth { get; }
        public float preferredHeight { get; }
    }

    public class CanvasScaler : Behaviour
    {
        public enum ScaleMode { ConstantPixelSize, ScaleWithScreenSize, ConstantPhysicalSize }
        public enum ScreenMatchMode { MatchWidthOrHeight, Expand, Shrink }
        public ScaleMode uiScaleMode { get; set; }
        public Vector2 referenceResolution { get; set; }
        public ScreenMatchMode screenMatchMode { get; set; }
        public float matchWidthOrHeight { get; set; }
    }

    public class GraphicRaycaster : Behaviour { }
}

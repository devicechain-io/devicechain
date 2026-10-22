// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A declaration-only stand-in for the Input System package (Unity.InputSystem). It declares ONLY the members the demo code touches, so a
// demo that starts using another member fails the compile check with a missing-member error, and
// the stub is extended in the same change. Never shipped, never run: it exists so the sources can be
// compiled against real UnityEngine reference assemblies with no Editor or licence, and it proves
// nothing about behaviour.
using System.Collections.Generic;
using UnityEngine;

namespace UnityEngine.InputSystem.Controls
{
    public class ButtonControl { public bool wasPressedThisFrame { get; } public bool isPressed { get; } }
    public class AnyKeyControl : ButtonControl { }
    public class Vector2Control { public Vector2 ReadValue() => default; }
    public class TouchControl { public ButtonControl press { get; } }
}

namespace UnityEngine.InputSystem
{
    using UnityEngine.InputSystem.Controls;

    public class InputControl { }

    public class Pointer
    {
        public static Pointer current { get; }
        public ButtonControl press { get; }
        public Vector2Control position { get; }
    }

    public class Mouse : Pointer
    {
        public static new Mouse current { get; }
        public ButtonControl leftButton { get; }
        public ButtonControl rightButton { get; }
    }

    public class Touchscreen : Pointer
    {
        public static new Touchscreen current { get; }
        public TouchControl primaryTouch { get; }
    }

    public class Gamepad
    {
        public static Gamepad current { get; }
        public IReadOnlyList<ButtonControl> allControls { get; }
    }

    public class Keyboard
    {
        public static Keyboard current { get; }
        public AnyKeyControl anyKey { get; }
        public ButtonControl rKey { get; } public ButtonControl lKey { get; } public ButtonControl tKey { get; }
        public ButtonControl pKey { get; } public ButtonControl kKey { get; } public ButtonControl dKey { get; }
        public ButtonControl iKey { get; } public ButtonControl zKey { get; } public ButtonControl gKey { get; }
        public ButtonControl fKey { get; } public ButtonControl yKey { get; } public ButtonControl nKey { get; }
        public ButtonControl escapeKey { get; } public ButtonControl spaceKey { get; } public ButtonControl homeKey { get; }
        public ButtonControl leftArrowKey { get; } public ButtonControl rightArrowKey { get; }
        public ButtonControl leftBracketKey { get; } public ButtonControl rightBracketKey { get; }
    }
}

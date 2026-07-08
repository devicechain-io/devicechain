// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Polyfill so `init`-only setters compile on netstandard2.1 (Unity's target). The type is
// baked into net5.0+; declaring it here is the standard shim, compiled out on modern TFMs.
#if !NET5_0_OR_GREATER
namespace System.Runtime.CompilerServices
{
    using System.ComponentModel;

    [EditorBrowsable(EditorBrowsableState.Never)]
    internal static class IsExternalInit
    {
    }
}
#endif

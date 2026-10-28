// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Towerpulse.Model
{
    /// <summary>
    /// A small seeded generator (SplitMix64). The same seed gives the same sequence on every runtime; System.Random does not promise that
    /// across .NET and Mono, and a trace that differs between the dotnet tests and the Editor would make the tests vouch for a different program.
    /// </summary>
    public sealed class DeterministicRng
    {
        ulong _state;

        public DeterministicRng(ulong seed) { _state = seed; }

        public ulong NextU64()
        {
            unchecked
            {
                _state += 0x9E3779B97F4A7C15UL;
                return Mix(_state);
            }
        }

        /// <summary>A uniform double in [0, 1).</summary>
        public double NextDouble() { return (NextU64() >> 11) * (1.0 / 9007199254740992.0); }

        /// <summary>A stateless hash of (seed, a, b) to [0, 1): a value that must not depend on how many draws came before it.</summary>
        public static double Hash01(ulong seed, int a, int b)
        {
            unchecked
            {
                ulong h = Mix(seed ^ 0xD1B54A32D192ED03UL);
                h = Mix(h + (ulong)(uint)a * 0x9E3779B97F4A7C15UL);
                h = Mix(h + (ulong)(uint)b * 0xBF58476D1CE4E5B9UL);
                return (h >> 11) * (1.0 / 9007199254740992.0);
            }
        }

        static ulong Mix(ulong z)
        {
            unchecked
            {
                z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9UL;
                z = (z ^ (z >> 27)) * 0x94D049BB133111EBUL;
                return z ^ (z >> 31);
            }
        }
    }
}

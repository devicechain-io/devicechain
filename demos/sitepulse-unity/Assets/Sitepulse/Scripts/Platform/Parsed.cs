// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>
    /// A value or the reason there is none. Configuration and wire parsing return this instead of
    /// throwing, so a refusal always reaches the screen with a message naming its file and field,
    /// and "invalid" can never be mistaken for a default.
    /// </summary>
    public sealed class Parsed<T>
    {
        Parsed(bool ok, T value, string error)
        {
            Ok = ok;
            Value = value;
            Error = error;
        }

        public bool Ok { get; }
        public T Value { get; }

        /// <summary>One problem per line.</summary>
        public string Error { get; }

        public static Parsed<T> Success(T value) => new Parsed<T>(true, value, null);
        public static Parsed<T> Fail(string error) => new Parsed<T>(false, default, error);
    }
}

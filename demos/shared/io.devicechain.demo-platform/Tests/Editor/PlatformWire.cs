// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Text;

namespace DeviceChain.Demos.Tests
{
    /// <summary>Canned wire shapes for the platform tests: JWT-shaped tokens.</summary>
    static class PlatformWire
    {
        static string B64(string s) => Convert.ToBase64String(Encoding.UTF8.GetBytes(s)).TrimEnd('=').Replace('+', '-').Replace('/', '_');

        /// <summary>A JWT-shaped string with the given payload; the signature is not a real one.</summary>
        public static string JwtWith(string payloadJson) => B64("{\"alg\":\"HS256\",\"typ\":\"JWT\"}") + "." + B64(payloadJson) + ".c2lnbmF0dXJl";

        public static string JwtExp(long exp) => JwtWith("{\"sub\":\"x\",\"exp\":" + exp + "}");
    }
}

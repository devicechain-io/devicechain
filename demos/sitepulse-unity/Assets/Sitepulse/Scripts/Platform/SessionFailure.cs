// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Sitepulse.Domain;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>
    /// How a session that could not start is worded for a person. The SDK throws a transport exception whose
    /// text is a paragraph of client id, address and library message; what the panel and the acceptance probe
    /// need is the one fact in it: the broker looked at this device's credential and said no. That is worded
    /// plainly, "refused by the broker (NotAuthorized)", and anything else keeps its (redacted) text.
    /// The raw text goes to the log, not the screen.
    /// </summary>
    public static class SessionFailure
    {
        /// <summary>The phrase a broker refusal always carries, so a reader (and the probe) can tell one from a network fault.</summary>
        public const string RefusedByBroker = "refused by the broker";

        static readonly string[] RefusalCodes = { "NotAuthorized", "BadUserNameOrPassword" };

        /// <summary>The broker's reason code when this exception text is a refusal at CONNECT; null when it is anything else.</summary>
        public static string BrokerRefusalCode(string text)
        {
            if (string.IsNullOrEmpty(text)) return null;
            foreach (var code in RefusalCodes)
                if (text.IndexOf(code, StringComparison.OrdinalIgnoreCase) >= 0) return code;
            if (text.IndexOf("not authorized", StringComparison.OrdinalIgnoreCase) >= 0) return RefusalCodes[0];
            return null;
        }

        /// <summary>The words the readiness line carries for a session that could not start.</summary>
        public static string Word(string exceptionText)
        {
            var code = BrokerRefusalCode(exceptionText);
            return code != null ? $"{RefusedByBroker} ({code})" : Redactor.Redact(exceptionText ?? "");
        }

        /// <summary>Whether a failure message (as <see cref="Word"/> wrote it, or the blind line) says the broker refused the device.</summary>
        public static bool SaysBrokerRefused(string failReason)
            => failReason != null
               && (failReason.IndexOf(RefusedByBroker, StringComparison.OrdinalIgnoreCase) >= 0
                   || failReason.IndexOf("the broker refused", StringComparison.OrdinalIgnoreCase) >= 0);
    }
}

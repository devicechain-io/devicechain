// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Demos.Platform
{
    /// <summary>
    /// The platform's nine command states, and <see cref="Unknown"/> for anything else (whose raw text
    /// is kept and printed). Showing a TIMEOUT or a CANCELLED as FAILED would say something the platform
    /// did not.
    /// </summary>
    public enum CommandState { Queued, Held, Sent, Parked, Successful, Failed, Timeout, Expired, Cancelled, Unknown }

    /// <summary>A command's state as the platform spelled it.</summary>
    public readonly struct CommandStatus
    {
        CommandStatus(CommandState state, string raw)
        {
            State = state;
            Raw = raw;
        }

        public CommandState State { get; }

        /// <summary>The text the platform sent; the only thing an Unknown state has to say.</summary>
        public string Raw { get; }

        public static CommandStatus Of(CommandState state) =>
            state == CommandState.Unknown ? throw new ArgumentException("an unknown state carries its raw text: use Parse", nameof(state)) : new CommandStatus(state, Name(state));

        /// <summary>Reads the platform's spelling (upper case). Anything else is Unknown, raw text kept.</summary>
        public static CommandStatus Parse(string raw)
        {
            switch (raw)
            {
                case "QUEUED": return Of(CommandState.Queued);
                case "HELD": return Of(CommandState.Held);
                case "SENT": return Of(CommandState.Sent);
                case "PARKED": return Of(CommandState.Parked);
                case "SUCCESSFUL": return Of(CommandState.Successful);
                case "FAILED": return Of(CommandState.Failed);
                case "TIMEOUT": return Of(CommandState.Timeout);
                case "EXPIRED": return Of(CommandState.Expired);
                case "CANCELLED": return Of(CommandState.Cancelled);
                default: return new CommandStatus(CommandState.Unknown, raw ?? "");
            }
        }

        /// <summary>The platform has finished with the command: it will not change state again.</summary>
        public bool IsTerminal => State == CommandState.Successful || State == CommandState.Failed
            || State == CommandState.Timeout || State == CommandState.Expired || State == CommandState.Cancelled;

        /// <summary>What a card prints: the platform's name for the state, or the raw text of an unknown one.</summary>
        public string Label => State == CommandState.Unknown ? (Raw.Length == 0 ? "UNKNOWN" : Raw) : Raw;

        static string Name(CommandState s) => s switch
        {
            CommandState.Queued => "QUEUED",
            CommandState.Held => "HELD",
            CommandState.Sent => "SENT",
            CommandState.Parked => "PARKED",
            CommandState.Successful => "SUCCESSFUL",
            CommandState.Failed => "FAILED",
            CommandState.Timeout => "TIMEOUT",
            CommandState.Expired => "EXPIRED",
            CommandState.Cancelled => "CANCELLED",
            _ => throw new ArgumentOutOfRangeException(nameof(s), s, null),
        };
    }
}

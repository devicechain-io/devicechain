// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text;

namespace DeviceChain.Towerpulse.Story
{
    /// <summary>
    /// A strict JSON reader for the small files this demo reads (the shot file), written here so the file can be parsed and validated by the engine-free
    /// tests and under IL2CPP with no serializer. Objects are <c>Dictionary&lt;string, object&gt;</c>, arrays <c>List&lt;object&gt;</c>, numbers <c>double</c>,
    /// plus <c>string</c>, <c>bool</c> and null. Duplicate keys, trailing text and unterminated input are errors, never guesses.
    /// </summary>
    public static class MiniJson
    {
        public sealed class ParseException : Exception
        {
            public ParseException(string message, int at) : base(message + " (at offset " + at + ")") { Offset = at; }
            public int Offset { get; }
        }

        public static object Parse(string text)
        {
            if (text == null) throw new ArgumentNullException(nameof(text));
            var p = new Reader(text);
            p.Skip();
            var v = p.Value(0);
            p.Skip();
            if (!p.End) throw new ParseException("unexpected text after the value", p.Pos);
            return v;
        }

        sealed class Reader
        {
            const int MaxDepth = 32;
            readonly string s;
            public int Pos;
            public Reader(string s) { this.s = s; }
            public bool End { get { return Pos >= s.Length; } }

            public void Skip()
            {
                while (Pos < s.Length && (s[Pos] == ' ' || s[Pos] == '\t' || s[Pos] == '\n' || s[Pos] == '\r')) Pos++;
            }

            char Peek()
            {
                if (Pos >= s.Length) throw new ParseException("unexpected end of input", Pos);
                return s[Pos];
            }

            public object Value(int depth)
            {
                if (depth > MaxDepth) throw new ParseException("nested too deeply", Pos);
                char c = Peek();
                if (c == '{') return Obj(depth);
                if (c == '[') return Arr(depth);
                if (c == '"') return Str();
                if (c == 't') { Lit("true"); return true; }
                if (c == 'f') { Lit("false"); return false; }
                if (c == 'n') { Lit("null"); return null; }
                return Num();
            }

            void Lit(string w)
            {
                if (string.CompareOrdinal(s, Pos, w, 0, w.Length) != 0) throw new ParseException("expected " + w, Pos);
                Pos += w.Length;
            }

            object Num()
            {
                int start = Pos;
                if (Peek() == '-') Pos++;
                while (Pos < s.Length && (char.IsDigit(s[Pos]) || s[Pos] == '.' || s[Pos] == 'e' || s[Pos] == 'E' || s[Pos] == '+' || s[Pos] == '-')) Pos++;
                var t = s.Substring(start, Pos - start);
                if (t.Length == 0 || !double.TryParse(t, NumberStyles.Float, CultureInfo.InvariantCulture, out var d) || double.IsNaN(d) || double.IsInfinity(d))
                    throw new ParseException("not a number: \"" + t + "\"", start);
                return d;
            }

            string Str()
            {
                Pos++; // opening quote
                var sb = new StringBuilder();
                while (true)
                {
                    char c = Peek();
                    Pos++;
                    if (c == '"') return sb.ToString();
                    if (c < ' ') throw new ParseException("control character in a string", Pos - 1);
                    if (c != '\\') { sb.Append(c); continue; }
                    char e = Peek();
                    Pos++;
                    switch (e)
                    {
                        case '"': sb.Append('"'); break;
                        case '\\': sb.Append('\\'); break;
                        case '/': sb.Append('/'); break;
                        case 'b': sb.Append('\b'); break;
                        case 'f': sb.Append('\f'); break;
                        case 'n': sb.Append('\n'); break;
                        case 'r': sb.Append('\r'); break;
                        case 't': sb.Append('\t'); break;
                        case 'u':
                            if (Pos + 4 > s.Length || !int.TryParse(s.Substring(Pos, 4), NumberStyles.HexNumber, CultureInfo.InvariantCulture, out var cp))
                                throw new ParseException("bad \\u escape", Pos);
                            sb.Append((char)cp);
                            Pos += 4;
                            break;
                        default: throw new ParseException("bad escape \\" + e, Pos - 1);
                    }
                }
            }

            List<object> Arr(int depth)
            {
                Pos++;
                var list = new List<object>();
                Skip();
                if (Peek() == ']') { Pos++; return list; }
                while (true)
                {
                    Skip();
                    list.Add(Value(depth + 1));
                    Skip();
                    char c = Peek();
                    Pos++;
                    if (c == ']') return list;
                    if (c != ',') throw new ParseException("expected , or ]", Pos - 1);
                }
            }

            Dictionary<string, object> Obj(int depth)
            {
                Pos++;
                var map = new Dictionary<string, object>(StringComparer.Ordinal);
                Skip();
                if (Peek() == '}') { Pos++; return map; }
                while (true)
                {
                    Skip();
                    if (Peek() != '"') throw new ParseException("expected a key", Pos);
                    int at = Pos;
                    var key = Str();
                    Skip();
                    if (Peek() != ':') throw new ParseException("expected :", Pos);
                    Pos++;
                    Skip();
                    if (map.ContainsKey(key)) throw new ParseException("duplicate key \"" + key + "\"", at);
                    map[key] = Value(depth + 1);
                    Skip();
                    char c = Peek();
                    Pos++;
                    if (c == '}') return map;
                    if (c != ',') throw new ParseException("expected , or }", Pos - 1);
                }
            }
        }
    }
}

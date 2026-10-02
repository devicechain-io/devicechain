# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# early-close-pipes.awk -- run by hack/shellcheck.sh over every tracked *.sh.
#
# Prints FILE:LINE:<pipeline> for every pipeline whose LAST reader can stop
# reading before its writer has finished: grep -q/-l/-L/-m (and their long
# spellings), head, read, an awk that exits, a sed that quits. Under pipefail
# the writer's resulting SIGPIPE -- or EPIPE, where SIGPIPE is ignored, as on
# GitHub's runners -- becomes the pipeline's status, and an `if` reads it as
# the reader's answer: "the module is in the list and grep stopped early" was
# reported as "the module is not in the list". Capture the writer's output
# first and test the captured text instead: grep -q... <<<"$(cmd)".
#
# The reader may carry a prefix: VAR=value, `command`, `env ...`,
# `timeout N`, or a `{` brace group.
#
# Full-line comments and heredoc bodies are skipped; lines ending in |, \ or
# && are joined, so a pipeline split across lines is reported once, at the
# line it starts on.
#
# 🔴 IT FAILS CLOSED ON A HEREDOC IT CANNOT SEE CLOSE. The heredoc detector does
# not understand quoting, so `echo "cat <<EOF"` or `$(( a << b ))` opens a
# heredoc that never ends, and without this every later line of the file would
# be skipped in silence -- a scanner whose failure mode is reporting clean. A
# heredoc still open at the end of a file is therefore reported as a finding.
# A false opener that later closes on a coincidental line cannot be detected
# this way; the lines between are not scanned.
#
# SCOPE: the tracked *.sh files hack/shellcheck.sh enumerates, nothing else.
# Workflow `run:` blocks and shell inside Makefiles are not scanned.
#
# KNOWN BLIND SPOTS. None occurs in the tree; they are listed so that nobody
# assumes coverage that is not there:
#   - `| while read ...; do ...; break; done`, a loop that stops early;
#   - an interpreter that reads part of its input (`| python3 -c '...'`);
#   - grep's -q family spelled through a variable, or written after a `)`,
#     `;` or `&` in grep's own arguments (`grep "$(x)" -q`): the argument
#     span stops there so that `[ "$(a | grep -c .)" -lt 2 ]` is not read
#     as grep -l.
function flush() {
  if (buf != "" && buf ~ early) printf "%s:%d:%s\n", bufFile, start, buf
  buf = ""
}
function unclosed() {
  if (tag != "")
    printf "%s:%d:heredoc <<%s opened here never closes; the rest of the file was not scanned\n", tagFile, tagLine, tag
  tag = ""
}
BEGIN {
  prefix = "([A-Za-z_][A-Za-z0-9_]*=[^|[:space:]]*[[:space:]]+" \
           "|command[[:space:]]+" \
           "|env([[:space:]]+[^|[:space:]]+)*[[:space:]]+" \
           "|timeout([[:space:]]+[^|[:space:]]+)+[[:space:]]+" \
           "|[{][[:space:]]*)*"
  early = "(^|[^|])[|][[:space:]]*" prefix "(" \
          "grep[^|;&)]*[[:space:]](-[A-Za-z0-9]*[qlLm]|--(quiet|silent|max-count|files-with-matches|files-without-match))" \
          "|head([[:space:]]|$|[);])" \
          "|read([[:space:]]|$)" \
          "|awk([^|]|[|][|])*[{;[:space:]]exit([[:space:]]|[;}]|$)" \
          "|sed[^|]*([;{0-9/$][[:space:]]*)[qQ]([[:space:];}'\"]|$)" \
          ")"
}
FNR == 1 { flush(); unclosed() }
tag != "" { if ($0 ~ ("^[[:space:]]*" tag "[[:space:]]*$")) tag = ""; next }
/^[[:space:]]*#/ { next }
{
  line = $0
  if (buf == "") { start = FNR; bufFile = FILENAME }
  buf = buf " " line
  if (match(line, /(^|[^<])<<-?[[:space:]]*['"]?[A-Za-z_][A-Za-z0-9_]*['"]?/)) {
    tag = substr(line, RSTART, RLENGTH)
    sub(/^[^<]*<<-?[[:space:]]*/, "", tag); gsub(/['"]/, "", tag)
    tagFile = FILENAME; tagLine = FNR
  }
  if (line ~ /([|\\]|&&)[[:space:]]*$/) next
  flush()
}
END { flush(); unclosed() }

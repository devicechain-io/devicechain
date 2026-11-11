// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

// The docs-command gate: every `dcctl` invocation in a shell block of the published
// docs must name a command that exists, with flags that command accepts and the number
// of arguments it takes, and every `hack/` path in one must exist in the tree.
// Local recursive patterns in go build/go vet must start inside a Go module.
// Literal local Helm installs must render successfully, entirely offline.
// Literal psql SELECT columns are checked against the frozen analytics projection.
//
// WHY A TEST IN THIS PACKAGE. The source of truth for dcctl is the cobra tree, and this
// is the one place that can ask it directly: rootCmd.Find resolves the command path the
// way the binary does, and each command's own flag set says what it accepts. A committed
// JSON of the tree would be one more artifact needing its own drift gate; a parser of
// the generated reference page would be checking the docs against the docs.
//
// WHAT IT CHECKS. Fenced blocks whose language is a shell (bash, sh, shell, zsh,
// console, shell-session) in docs/docs and every docs/i18n/<locale>/.../current tree.
// A bare ``` fence is NOT read: the docs use it for command output too, and output such
// as "dcctl stopped waiting for ..." would read as a command. A command written in a
// bare fence is outside the gate until its fence says `bash`. The repository README is
// read too. The generated command reference is not: it is rendered from the same tree.
//
// A `<placeholder>` is a value and passes wherever a value goes; a placeholder where a
// command NAME goes (`dcctl <command> --help`), or one that stands for several words
// (`dcctl install <the flags it was installed with>`), makes the invocation a template,
// which is skipped and listed. hack/ paths are resolved from the repository root. Only
// hack/ is checked as a path because other top-level names collide with shell syntax:
// `kubectl port-forward deploy/device-management` names a Deployment, not ./deploy.
//
// 🔴 SCOPE, said up front so a green run is not read as more than it is: it proves a
// dcctl command NAME, its FLAG names and its ARITY, and literal Go module locations.
// Local Helm installs are rendered offline with supplied values and reported stand-ins
// for whole environment references and image.tag placeholders.
// SQL covers one analytics view and a small unqualified expression subset; other
// syntax and release-history pages are visibly skipped. It never executes SQL.
// It does not otherwise prove a flag's value parses,
// that the command succeeds, or that the sentence beside it is true. Every other tool
// (kubectl, curl, ...) is counted, not checked, and the count is printed, so the
// uncovered surface is visible rather than implied:
//
//	cd backend/cli && go test ./cmd -run TestPublishedDocsCommandsExist -v -count=1
//
// Go module locations use literal cd state within each fence, starting at the repo
// root, and local go.work/go.mod metadata. Dynamic directories and loops are listed
// as skipped. This is not a shell interpreter or a build/flag/package-existence check.
//
// 🔴 A SCANNER FAILS BY REPORTING CLEAN, so the real-tree run refuses to pass if it
// found fewer invocations per locale than a floor, and the negative controls below plant
// a bogus `dcctl frobnicate --nope` and require it to be named.

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// repoRoot is the repository root, relative to this package.
var repoRoot = filepath.Join("..", "..", "..")

// shellFenceLangs are the fence languages read as shell.
var shellFenceLangs = map[string]bool{
	"bash": true, "sh": true, "shell": true, "zsh": true, "console": true, "shell-session": true,
}

// promptOnlyLangs are the languages where only `$ `-prompted lines are commands and
// the rest is output.
var promptOnlyLangs = map[string]bool{"console": true, "shell-session": true}

// The floors below which the real-tree run refuses to pass, per docs tree. They guard
// against the EXTRACTOR breaking (a fence syntax it stops recognizing, a walk rooted in
// the wrong place), which would otherwise turn every finding into silence and report
// clean. They are not coverage targets: when the gate landed each tree held 48 dcctl
// invocations and 5 hack/ paths, so a floor trips only on losing most of them.
const (
	minDcctlInvocationsPerTree = 40
	minHackPathsPerTree        = 3
)

type shellBlock struct {
	file      string
	startLine int // 1-based line of the first content line
	lang      string
	lines     []string
}

// shellBlocks returns the shell-language fenced blocks of a markdown document.
func shellBlocks(file, text string) []shellBlock {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var out []shellBlock
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimLeft(lines[i], " \t")
		ch, n := fenceRun(trimmed)
		if n < 3 {
			continue
		}
		indent := len(lines[i]) - len(trimmed)
		lang := ""
		if f := strings.Fields(trimmed[n:]); len(f) > 0 {
			lang = strings.ToLower(f[0])
		}
		var body []string
		j := i + 1
		for ; j < len(lines); j++ {
			t := strings.TrimLeft(lines[j], " \t")
			if c2, n2 := fenceRun(t); c2 == ch && n2 >= n && strings.TrimSpace(t[n2:]) == "" {
				break
			}
			l := lines[j]
			// Content of an indented fence (inside a list item) is indented by the same amount.
			k := 0
			for k < indent && k < len(l) && (l[k] == ' ' || l[k] == '\t') {
				k++
			}
			body = append(body, l[k:])
		}
		if shellFenceLangs[lang] {
			out = append(out, shellBlock{file: file, startLine: i + 2, lang: lang, lines: body})
		}
		i = j
	}
	return out
}

// shellLine is one logical command line of a block (continuations joined).
type shellLine struct {
	line int
	text string
}

var heredocRe = regexp.MustCompile(`(?:^|[^<])<<-?\s*(?:'([A-Za-z_][A-Za-z0-9_]*)'|"([A-Za-z_][A-Za-z0-9_]*)"|([A-Za-z_][A-Za-z0-9_]*))`)

// logicalLines joins backslash continuations, drops `$ ` prompts (and, for console
// blocks, everything that is not prompted), and skips heredoc bodies, which are data.
func logicalLines(b shellBlock) []shellLine {
	var out []shellLine
	var pending []string
	pendingLine := 0
	heredoc := ""
	for idx, raw := range b.lines {
		lineNo := b.startLine + idx
		if heredoc != "" {
			if strings.TrimSpace(raw) == heredoc {
				heredoc = ""
			}
			continue
		}
		l := raw
		if len(pending) == 0 {
			t := strings.TrimLeft(l, " \t")
			switch {
			case strings.HasPrefix(t, "$ "):
				l = t[2:]
			case promptOnlyLangs[b.lang]:
				continue
			}
			pendingLine = lineNo
		}
		if strings.HasSuffix(strings.TrimRight(l, " \t"), `\`) {
			t := strings.TrimRight(l, " \t")
			pending = append(pending, t[:len(t)-1])
			continue
		}
		pending = append(pending, l)
		// A quoted string that spans lines (a JSON body, a SQL query) is one word.
		if unclosedQuote(strings.Join(pending, "\n")) {
			continue
		}
		text := strings.Join(pending, " ")
		pending = nil
		out = append(out, shellLine{line: pendingLine, text: text})
		if m := heredocRe.FindStringSubmatch(stripComment(text)); m != nil {
			heredoc = m[1] + m[2] + m[3]
		}
	}
	if len(pending) > 0 {
		out = append(out, shellLine{line: pendingLine, text: strings.Join(pending, " ")})
	}
	return out
}

// unclosedQuote reports whether s ends inside a quoted string.
func unclosedQuote(s string) bool {
	_, open := scanQuotes(s)
	return open
}

// stripComment removes an unquoted `#` comment that starts a word.
func stripComment(s string) string {
	cut, _ := scanQuotes(s)
	return s[:cut]
}

// scanQuotes walks s with shell quoting and returns where an unquoted `#` comment
// starts (len(s) if none) and whether the text ends inside a quote.
func scanQuotes(s string) (int, bool) {
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case quote == '"':
			if c == '\\' {
				i++
			} else if c == '"' {
				quote = 0
			}
		case c == '\\':
			i++
		case c == '\'' || c == '"':
			quote = c
		case c == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t' || s[i-1] == ';'):
			return i, false
		}
	}
	return len(s), quote != 0
}

// substPlaceholder stands in for a command substitution's output in the outer command.
const substPlaceholder = "__SUBST__"

// extractSubstitutions replaces each `$(...)` and backtick substitution with a
// placeholder word and returns the inner commands separately, so `X=$(dcctl ...)` is
// checked as a dcctl invocation.
func extractSubstitutions(s string) (string, []string) {
	var inner []string
	var b strings.Builder
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote == '\'' {
			b.WriteByte(c)
			if c == '\'' {
				quote = 0
			}
			continue
		}
		if c == '\\' && i+1 < len(s) {
			b.WriteByte(c)
			b.WriteByte(s[i+1])
			i++
			continue
		}
		if c == '\'' && quote == 0 {
			quote = '\''
			b.WriteByte(c)
			continue
		}
		if c == '"' {
			if quote == '"' {
				quote = 0
			} else {
				quote = '"'
			}
			b.WriteByte(c)
			continue
		}
		if c == '$' && i+1 < len(s) && s[i+1] == '(' && !(i+2 < len(s) && s[i+2] == '(') {
			depth := 0
			j := i + 1
			for ; j < len(s); j++ {
				if s[j] == '(' {
					depth++
				} else if s[j] == ')' {
					depth--
					if depth == 0 {
						break
					}
				}
			}
			body := s[i+2 : min(j, len(s))]
			sub, nested := extractSubstitutions(body)
			inner = append(inner, sub)
			inner = append(inner, nested...)
			b.WriteString(substPlaceholder)
			i = j
			continue
		}
		if c == '`' {
			j := strings.IndexByte(s[i+1:], '`')
			if j >= 0 {
				inner = append(inner, s[i+1:i+1+j])
				b.WriteString(substPlaceholder)
				i = i + 1 + j
				continue
			}
		}
		b.WriteByte(c)
	}
	return b.String(), inner
}

// splitSegments lexes one command line into simple commands: words with quotes
// removed, split at `&&`, `||`, `;`, `|`, `&`, `(` and `)`, with redirections and their
// targets dropped.
func splitSegments(s string) [][]string {
	return shellSegments(s, false)
}

// shellSegments can retain subshell boundaries for working-directory tracking.
func shellSegments(s string, groups bool) [][]string {
	var segs [][]string
	var cur []string
	var word strings.Builder
	inWord := false
	dropNext := false
	flushWord := func() {
		if !inWord {
			return
		}
		if dropNext {
			dropNext = false
		} else {
			cur = append(cur, word.String())
		}
		word.Reset()
		inWord = false
	}
	endSeg := func() {
		flushWord()
		if len(cur) > 0 {
			segs = append(segs, cur)
		}
		cur = nil
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			inWord = true
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				j = len(s) - i - 1
			}
			word.WriteString(s[i+1 : i+1+j])
			i += j + 1
		case c == '"':
			inWord = true
			for i++; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				word.WriteByte(s[i])
			}
		case c == '\\' && i+1 < len(s):
			inWord = true
			i++
			word.WriteByte(s[i])
		case c == ' ' || c == '\t':
			flushWord()
		case c == ';' || c == '|' || c == '&' || c == '(' || c == ')':
			if c == '&' && i > 0 && (s[i-1] == '>' || s[i-1] == '<') {
				// part of a >& redirection, handled below
				continue
			}
			endSeg()
			if groups && (c == '(' || c == ')') {
				segs = append(segs, []string{string(c)})
			}
			if (c == '&' || c == '|') && i+1 < len(s) && s[i+1] == c {
				i++
			}
		case c == '<' && placeholderEnd(s, i) > 0:
			// <instance>, <the flags it was installed with>: a placeholder word, not a redirection.
			j := placeholderEnd(s, i)
			inWord = true
			word.WriteString(s[i : j+1])
			i = j
		case c == '>' || c == '<':
			// A digit-only word right before is the fd (2>&1): drop it.
			if inWord && strings.Trim(word.String(), "0123456789") == "" {
				word.Reset()
				inWord = false
			} else {
				flushWord()
			}
			for i+1 < len(s) && (s[i+1] == '>' || s[i+1] == '<' || s[i+1] == '&' || s[i+1] == '-') {
				i++
			}
			// `>&1` names an fd, not a file.
			if s[i] == '&' {
				for i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9' {
					i++
				}
				continue
			}
			dropNext = true
		default:
			inWord = true
			word.WriteByte(c)
		}
	}
	endSeg()
	return segs
}

// placeholderEnd returns the index of the `>` closing a placeholder that opens at
// s[i] == '<', or -1. A placeholder starts with a letter and ends before any shell
// operator; `<in >out` is two redirections, not one placeholder.
func placeholderEnd(s string, i int) int {
	if i+1 >= len(s) || !(s[i+1] >= 'a' && s[i+1] <= 'z' || s[i+1] >= 'A' && s[i+1] <= 'Z') {
		return -1
	}
	for j := i + 2; j < len(s); j++ {
		switch s[j] {
		case '>':
			if s[j-1] == ' ' {
				return -1
			}
			return j
		case '<', '|', ';', '&', '(', ')', '\'', '"':
			return -1
		}
	}
	return -1
}

var envAssignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// commandPrefixes are words that precede the command a segment runs.
var commandPrefixes = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "do": true, "while": true, "until": true,
	"!": true, "{": true, "}": true, "time": true, "sudo": true, "exec": true, "env": true,
	"command": true, "nohup": true,
}

// commandWords drops the prefixes and environment assignments before a segment's
// command, returning the command and its arguments.
func commandWords(seg []string) []string {
	for len(seg) > 0 && (commandPrefixes[seg[0]] || envAssignRe.MatchString(seg[0])) {
		seg = seg[1:]
	}
	return seg
}

func isDcctl(word string) bool {
	base := path.Base(strings.ReplaceAll(word, `\`, "/"))
	return base == "dcctl" || base == "dcctl.exe"
}

// isPlaceholder reports a word the docs use to stand for a value: <instance>,
// [flags], {provider}, ... — it says "something goes here", not a name to check.
func isPlaceholder(w string) bool {
	if w == "..." || w == "…" {
		return true
	}
	if strings.Contains(w, "<") && strings.Contains(w, ">") {
		return true
	}
	return (strings.HasPrefix(w, "[") && strings.HasSuffix(w, "]")) ||
		(strings.HasPrefix(w, "{") && strings.HasSuffix(w, "}"))
}

// inertValue stands in for a flag's value so that parsing the docs' arguments can
// never write into the real command's flag variables.
type inertValue struct{ typ string }

func (v inertValue) String() string   { return "" }
func (v inertValue) Set(string) error { return nil }
func (v inertValue) Type() string     { return v.typ }

// checkDcctl validates one invocation's words (after `dcctl`) against the cobra tree.
// It returns a problem, or "" — and skipped=true when the invocation is a template
// rather than a command (a placeholder where a command name goes).
func checkDcctl(root *cobra.Command, args []string) (problem string, skipped bool) {
	// `dcctl <command> --help`: the command position is a placeholder.
	var cleaned []string
	for _, a := range args {
		if strings.HasPrefix(a, "[") && strings.HasSuffix(a, "]") || a == "..." || a == "…" {
			continue // "[flags]", "...": usage notation, not an argument
		}
		cleaned = append(cleaned, a)
	}
	c, rest, err := root.Find(cleaned)
	if err != nil {
		if len(rest) > 0 && isPlaceholder(rest[0]) {
			return "", true
		}
		return err.Error(), false
	}
	// `dcctl install <the flags the cluster was installed with>`: the command is real,
	// the rest is prose standing in for arguments, so there is no arity to check.
	for _, a := range rest {
		if isPlaceholder(a) && strings.ContainsAny(a, " \t") {
			return "", true
		}
	}

	fs := pflag.NewFlagSet(c.CommandPath(), pflag.ContinueOnError)
	fs.SetOutput(discard{})
	add := func(f *pflag.Flag) {
		if fs.Lookup(f.Name) != nil {
			return
		}
		short := f.Shorthand
		if short != "" && fs.ShorthandLookup(short) != nil {
			short = ""
		}
		fs.AddFlag(&pflag.Flag{Name: f.Name, Shorthand: short, Usage: f.Usage,
			Value: inertValue{f.Value.Type()}, NoOptDefVal: f.NoOptDefVal})
	}
	c.Flags().VisitAll(add)
	c.InheritedFlags().VisitAll(add)
	if fs.Lookup("help") == nil {
		short := "h"
		if fs.ShorthandLookup("h") != nil {
			short = ""
		}
		fs.AddFlag(&pflag.Flag{Name: "help", Shorthand: short, Value: inertValue{"bool"}, NoOptDefVal: "true"})
	}

	var flagArgs []string
	for _, a := range rest {
		if strings.HasPrefix(a, "-") && isPlaceholder(a) {
			return "", true // --<flag>
		}
		flagArgs = append(flagArgs, a)
	}
	if err := fs.Parse(flagArgs); err != nil {
		return fmt.Sprintf("%s: %v", c.CommandPath(), err), false
	}
	if fs.Changed("help") {
		return "", false
	}
	positionals := fs.Args()
	if c.HasSubCommands() && !c.Runnable() {
		if len(positionals) > 0 {
			if isPlaceholder(positionals[0]) {
				return "", true
			}
			return fmt.Sprintf("unknown command %q for %q", positionals[0], c.CommandPath()), false
		}
		return "", false
	}
	if c.Args != nil {
		if err := c.Args(c, positionals); err != nil {
			return fmt.Sprintf("%s: %v", c.CommandPath(), err), false
		}
	}
	return "", false
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// hackPathRe finds a repo-relative hack/ path: a word that begins with it, optionally
// after ./, and not as the tail of some other path.
var hackPathRe = regexp.MustCompile(`(?:^|[\s"'=(:])((?:\./)?hack/[A-Za-z0-9_][A-Za-z0-9_./-]*)`)

type docsFinding struct {
	where string // file:line
	text  string // the invocation or path
	msg   string
}

// docsGoLayout reads only local metadata. Workspace use entries are module roots;
// go.mod files also recognize modules outside the workspace (for example fixtures).
// No published command, go list, or toolchain download is needed.
type docsGoLayout struct {
	repo  string
	roots map[string]bool
}

func readDocsGoLayout(repo string) (*docsGoLayout, error) {
	b, err := os.ReadFile(filepath.Join(repo, "go.work"))
	if err != nil {
		return nil, err
	}
	l := &docsGoLayout{repo: repo, roots: map[string]bool{}}
	inUse := false
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "//", 2)[0])
		if line == "use (" {
			inUse = true
			continue
		}
		if line == ")" {
			inUse = false
			continue
		}
		if strings.HasPrefix(line, "use ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "use "))
		} else if !inUse {
			continue
		}
		if words := splitSegments(line); len(words) == 1 && len(words[0]) == 1 && literalDocsPath(words[0][0]) {
			l.roots[path.Clean(words[0][0])] = true
		}
	}
	return l, nil
}

func literalDocsPath(p string) bool {
	return p != "" && p != "-" && !path.IsAbs(p) &&
		!strings.ContainsAny(p, "$`~*?[]{}<>:\\") && !strings.Contains(p, substPlaceholder)
}

func (l *docsGoLayout) directory(p string) bool {
	if p == ".." || strings.HasPrefix(p, "../") {
		return false
	}
	info, err := os.Stat(filepath.Join(l.repo, filepath.FromSlash(p)))
	return err == nil && info.IsDir()
}

func (l *docsGoLayout) inModule(p string) bool {
	for {
		if l.roots[p] {
			return true
		}
		if info, err := os.Stat(filepath.Join(l.repo, filepath.FromSlash(p), "go.mod")); err == nil && !info.IsDir() {
			return true
		}
		if p == "." {
			return false
		}
		p = path.Dir(p)
	}
}

// An empty directory is unresolved, not the repository root. Each fence starts at
// the repository root; cd - and subshells preserve the shell's literal directory state.
type docsWorkingDir struct{ dir, previous string }

func (s *docsScan) scanGoCommands(text, where string, wd *docsWorkingDir, stack *[]docsWorkingDir, loops *int) {
	for _, seg := range shellSegments(text, true) {
		switch seg[0] {
		case "(":
			*stack = append(*stack, *wd)
			continue
		case ")":
			if n := len(*stack); n > 0 {
				*wd = (*stack)[n-1]
				*stack = (*stack)[:n-1]
			}
			continue
		case "for", "select", "while", "until":
			*loops++
			wd.dir = ""
		case "done":
			if *loops > 0 {
				*loops--
			}
			continue
		}
		words := commandWords(seg)
		if len(words) == 0 {
			continue
		}
		if words[0] == "cd" {
			next := ""
			args := words[1:]
			if len(args) > 0 && args[0] == "--" {
				args = args[1:]
			}
			if *loops == 0 && len(args) == 1 {
				if args[0] == "-" {
					next = wd.previous
				} else if wd.dir != "" && literalDocsPath(args[0]) {
					candidate := path.Join(wd.dir, args[0])
					layout := s.goLayout
					if layout == nil {
						layout = &docsGoLayout{repo: s.helmRepo}
					}
					if layout.directory(candidate) {
						next = candidate
					}
				}
			}
			wd.previous, wd.dir = wd.dir, next
			continue
		}
		if docsHelmInstall(words) {
			s.scanHelmInstall(words, where, wd.dir, *loops)
			continue
		}
		if s.goLayout == nil || !docsGoRecursiveCommand(words) {
			continue
		}
		invocation := strings.Join(words, " ")
		reason := ""
		if *loops > 0 {
			reason = "dynamic loop"
		} else if wd.dir == "" {
			reason = "unresolved working directory"
		}
		for _, arg := range words[2:] {
			if arg == "-C" || strings.HasPrefix(arg, "-C=") {
				// go -C changes the directory the patterns resolve from; not modelled.
				reason = "go -C"
			}
		}
		unresolvedWD := reason != ""
		var invalid []string
		for _, arg := range words[2:] {
			if !strings.HasPrefix(arg, "./") && !strings.HasPrefix(arg, "../") || !strings.HasSuffix(arg, "/...") {
				continue
			}
			if !literalDocsPath(arg) {
				reason = "dynamic package pattern"
				continue
			}
			if unresolvedWD {
				continue
			}
			target := path.Join(wd.dir, strings.TrimSuffix(arg, "/..."))
			if !s.goLayout.directory(target) {
				reason = "unresolved pattern directory"
			} else if !s.goLayout.inModule(target) {
				invalid = append(invalid, arg)
			}
		}
		if len(invalid) > 0 {
			s.findings = append(s.findings, docsFinding{where, invocation,
				fmt.Sprintf("local recursive pattern(s) %s from %s start outside a Go module; run from a module directory or name a module-rooted pattern", strings.Join(invalid, ", "), wd.dir)})
		}
		if reason != "" {
			s.goSkipped = append(s.goSkipped, where+": "+invocation+" ("+reason+")")
		} else {
			s.goChecked++
		}
	}
}

func docsGoRecursiveCommand(words []string) bool {
	if len(words) < 3 || words[0] != "go" || (words[1] != "build" && words[1] != "vet") {
		return false
	}
	for _, arg := range words[2:] {
		if (strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, "../")) && strings.HasSuffix(arg, "/...") {
			return true
		}
	}
	return false
}

type docsScan struct {
	findings     []docsFinding
	dcctlChecked int
	dcctlSkipped []string // where: text, for templates
	hackChecked  int
	goChecked    int
	goSkipped    []string
	goLayout     *docsGoLayout
	helmRepo     string
	helmChecked  int
	helmSkipped  []string
	helmStandIns []string
	sqlChecked   int
	sqlSkipped   []string
	sqlRepo      string

	uncovered map[string]int // first word -> count
}

// scanDocsText runs the gate over one markdown document. exists answers whether a
// repo-relative path exists.
func scanDocsText(root *cobra.Command, file, text string, exists func(string) bool, s *docsScan) {
	for _, b := range shellBlocks(file, text) {
		wd := docsWorkingDir{dir: "."}
		var stack []docsWorkingDir
		loops := 0
		for _, ln := range logicalLines(b) {
			where := fmt.Sprintf("%s:%d", file, ln.line)

			for _, m := range hackPathRe.FindAllStringSubmatch(ln.text, -1) {
				p := strings.TrimRight(strings.TrimPrefix(m[1], "./"), ".,:;")
				s.hackChecked++
				if !exists(p) {
					s.findings = append(s.findings, docsFinding{where, p, "no such path in the repository"})
				}
			}

			outer, inner := extractSubstitutions(stripComment(ln.text))
			// Extraction separates substitutions from their position in the line.
			// Their execution directory may follow an earlier cd on that line, so
			// leave it unresolved rather than guess. They cannot change outer state.
			for _, sub := range inner {
				subWD, subLoops := wd, loops
				subWD.dir = ""
				var subStack []docsWorkingDir
				s.scanGoCommands(sub, where, &subWD, &subStack, &subLoops)
			}
			s.scanGoCommands(outer, where, &wd, &stack, &loops)
			for _, cmdline := range append([]string{outer}, inner...) {
				for _, seg := range splitSegments(cmdline) {
					words := commandWords(seg)
					if len(words) == 0 {
						continue
					}
					if !isDcctl(words[0]) {
						if words[0] == "psql" {
							s.scanPsql(words, where)
							continue
						}
						if docsHelmInstall(words) || s.goLayout != nil && docsGoRecursiveCommand(words) {
							continue // counted by the Helm or module-location check
						}
						if !hackPathRe.MatchString(words[0]) { // a hack/ script is checked above
							s.uncovered[words[0]]++
						}
						continue
					}
					text := "dcctl " + strings.Join(words[1:], " ")
					text = strings.TrimSpace(text)
					problem, skipped := checkDcctl(root, words[1:])
					switch {
					case skipped:
						s.dcctlSkipped = append(s.dcctlSkipped, where+": "+text)
					case problem != "":
						s.dcctlChecked++
						s.findings = append(s.findings, docsFinding{where, text, problem})
					default:
						s.dcctlChecked++
					}
				}
			}
		}
	}
}

func newDocsScan() *docsScan {
	return &docsScan{uncovered: map[string]int{}, helmRepo: repoRoot, sqlRepo: repoRoot}
}

// docsTrees returns docs/docs and every locale's current tree under repo, relative to it.
// The frozen versioned snapshots (docs/versioned_docs, .../version-X.Y) are left out on
// purpose: they record the commands a past release shipped, and checking them against
// today's command tree would fail the moment a command is renamed.
func docsTrees(repo string) ([]string, error) {
	trees := []string{filepath.Join("docs", "docs")}
	locales, err := filepath.Glob(filepath.Join(repo, "docs", "i18n", "*", "docusaurus-plugin-content-docs", "current"))
	if err != nil {
		return nil, err
	}
	for _, l := range locales {
		rel, err := filepath.Rel(repo, l)
		if err != nil {
			return nil, err
		}
		trees = append(trees, rel)
	}
	sort.Strings(trees)
	return trees, nil
}

// checkPublishedDocs runs the gate over the published docs of the repository at repo.
// It returns every problem (a finding, a missing translation tree, a tree below its
// floor) and the report of what was and was not checked.
func checkPublishedDocs(root *cobra.Command, repo string) (problems, report []string, err error) {
	layout, err := readDocsGoLayout(repo)
	if err != nil {
		return nil, nil, err
	}
	exists := func(rel string) bool {
		_, err := os.Stat(filepath.Join(repo, filepath.FromSlash(rel)))
		return err == nil
	}
	trees, err := docsTrees(repo)
	if err != nil {
		return nil, nil, err
	}
	if len(trees) < 3 {
		problems = append(problems, fmt.Sprintf("found %d docs trees (%v); expected English plus the es and zh-CN translations", len(trees), trees))
	}
	// The repository README is published too (it is the project's front page), but it
	// is one page and carries no floor.
	floors := map[string]bool{}
	for _, tree := range trees {
		floors[tree] = true
	}
	if _, err := os.Stat(filepath.Join(repo, "README.md")); err == nil {
		trees = append(trees, "README.md")
	}

	total := newDocsScan()
	for _, tree := range trees {
		s := newDocsScan()
		s.goLayout = layout
		s.helmRepo = repo
		s.sqlRepo = repo
		err := filepath.WalkDir(filepath.Join(repo, tree), func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !(strings.HasSuffix(p, ".md") || strings.HasSuffix(p, ".mdx")) {
				return nil
			}
			rel, err := filepath.Rel(repo, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			// The command reference is generated from the tree this gate checks against;
			// it is kept current by TestCommittedReferenceIsCurrent, not here.
			if strings.HasSuffix(rel, "/reference/dcctl.md") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			scanDocsText(root, rel, string(b), exists, s)
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
		tree = filepath.ToSlash(tree)
		if n := s.dcctlChecked + len(s.dcctlSkipped); floors[filepath.FromSlash(tree)] && n < minDcctlInvocationsPerTree {
			problems = append(problems, fmt.Sprintf("%s: found only %d dcctl invocations (floor %d); the extractor is losing blocks, not the docs commands",
				tree, n, minDcctlInvocationsPerTree))
		}
		if floors[filepath.FromSlash(tree)] && s.hackChecked < minHackPathsPerTree {
			problems = append(problems, fmt.Sprintf("%s: found only %d hack/ paths (floor %d); the extractor is losing blocks, not the docs paths",
				tree, s.hackChecked, minHackPathsPerTree))
		}
		report = append(report, fmt.Sprintf("%s: %d dcctl invocations checked, %d skipped as templates, %d hack/ paths checked",
			tree, s.dcctlChecked, len(s.dcctlSkipped), s.hackChecked))
		report = append(report, fmt.Sprintf("%s: %d Go module-location commands checked, %d skipped", tree, s.goChecked, len(s.goSkipped)))
		total.findings = append(total.findings, s.findings...)
		total.dcctlChecked += s.dcctlChecked
		total.dcctlSkipped = append(total.dcctlSkipped, s.dcctlSkipped...)
		total.hackChecked += s.hackChecked
		report = append(report, fmt.Sprintf("%s: %d Helm installs checked, %d skipped", tree, s.helmChecked, len(s.helmSkipped)))
		total.helmChecked += s.helmChecked
		total.helmStandIns = append(total.helmStandIns, s.helmStandIns...)
		total.helmSkipped = append(total.helmSkipped, s.helmSkipped...)
		total.goChecked += s.goChecked
		total.goSkipped = append(total.goSkipped, s.goSkipped...)
		report = append(report, fmt.Sprintf("%s: %d SQL SELECTs checked, %d skipped", tree, s.sqlChecked, len(s.sqlSkipped)))
		total.sqlChecked += s.sqlChecked
		total.sqlSkipped = append(total.sqlSkipped, s.sqlSkipped...)
		for k, v := range s.uncovered {
			total.uncovered[k] += v
		}
	}

	for _, f := range total.findings {
		problems = append(problems, fmt.Sprintf("%s: `%s`: %s", f.where, f.text, f.msg))
	}
	for _, s := range total.dcctlSkipped {
		report = append(report, "skipped (template): "+s)
	}
	for _, skipped := range total.sqlSkipped {
		report = append(report, "skipped (SQL SELECT): "+skipped)
	}
	report = append(report, fmt.Sprintf("SQL SELECT: %d checked, %d skipped", total.sqlChecked, len(total.sqlSkipped)))
	for _, s := range total.goSkipped {
		report = append(report, "skipped (Go module location): "+s)
	}
	report = append(report, fmt.Sprintf("Go module location: %d checked, %d skipped", total.goChecked, len(total.goSkipped)))
	report = append(report, total.helmStandIns...)
	for _, skipped := range total.helmSkipped {
		report = append(report, "skipped (Helm render): "+skipped)
	}
	report = append(report, fmt.Sprintf("Helm render: %d checked, %d skipped", total.helmChecked, len(total.helmSkipped)))
	type kv struct {
		k string
		v int
	}
	var unc []kv
	sum := 0
	for k, v := range total.uncovered {
		unc = append(unc, kv{k, v})
		sum += v
	}
	sort.Slice(unc, func(i, j int) bool { return unc[i].v > unc[j].v || unc[i].v == unc[j].v && unc[i].k < unc[j].k })
	var parts []string
	for _, e := range unc {
		parts = append(parts, fmt.Sprintf("%s=%d", e.k, e.v))
	}
	report = append(report,
		fmt.Sprintf("NOT CHECKED: %d other shell commands across %d trees: %s", sum, len(trees), strings.Join(parts, " ")),
		fmt.Sprintf("checked %d dcctl invocations and %d hack/ paths; skipped %d dcctl templates; %d findings",
			total.dcctlChecked, total.hackChecked, len(total.dcctlSkipped), len(total.findings)))
	return problems, report, nil
}

// TestPublishedDocsCommandsExist is the gate over the real docs.
func TestPublishedDocsCommandsExist(t *testing.T) {
	rootCmd.InitDefaultHelpCmd()
	rootCmd.InitDefaultCompletionCmd()
	problems, report, err := checkPublishedDocs(rootCmd, repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range report {
		t.Log(line)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// TestPublishedDocsGateFailsOnAPlantedTree runs the whole-tree path — walk, floors,
// verdict — over a synthetic repository, so the part of the gate that turns findings
// into a failure is itself shown to fail, not only the per-line checks.
func TestPublishedDocsGateFailsOnAPlantedTree(t *testing.T) {
	rootCmd.InitDefaultHelpCmd()
	rootCmd.InitDefaultCompletionCmd()
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.work"), []byte("go 1.27.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	locales := []string{
		filepath.Join("docs", "docs"),
		filepath.Join("docs", "i18n", "es", "docusaurus-plugin-content-docs", "current"),
		filepath.Join("docs", "i18n", "zh-CN", "docusaurus-plugin-content-docs", "current"),
	}
	if err := os.MkdirAll(filepath.Join(repo, "hack"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "hack", "real.sh"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var page strings.Builder
	page.WriteString("```bash\n")
	for i := 0; i < minDcctlInvocationsPerTree; i++ {
		page.WriteString("dcctl bootstrap local prod --yes\n")
	}
	for i := 0; i < minHackPathsPerTree; i++ {
		page.WriteString("hack/real.sh\n")
	}
	page.WriteString("```\n")
	for _, l := range locales {
		if err := os.MkdirAll(filepath.Join(repo, l), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, l, "page.md"), []byte(page.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	problems, _, err := checkPublishedDocs(rootCmd, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("a clean tree failed: %v", problems)
	}

	// A bogus command in the Spanish tree only.
	es := filepath.Join(repo, locales[1], "bad.md")
	if err := os.WriteFile(es, []byte("```bash\ndcctl frobnicate --nope\n```\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	problems, _, _ = checkPublishedDocs(rootCmd, repo)
	if len(problems) != 1 || !strings.Contains(problems[0], "docs/i18n/es/") || !strings.Contains(problems[0], `unknown command "frobnicate"`) {
		t.Errorf("a bogus command in one locale was not the one problem reported: %v", problems)
	}
	if err := os.Remove(es); err != nil {
		t.Fatal(err)
	}

	// The README goes through the same module-location verdict and skip reporting.
	readme := filepath.Join(repo, "README.md")
	if err := os.WriteFile(readme, []byte("```bash\ngo build ./...\ncd \"$module\"\ngo vet ./...\nhelm install dc external/chart\npsql -f query.sql\n```\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	problems, report, err := checkPublishedDocs(rootCmd, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "README.md:2") || !strings.Contains(problems[0], "outside a Go module") {
		t.Errorf("a root Go pattern was not the one problem reported: %v", problems)
	}
	if got := strings.Join(report, "\n"); !strings.Contains(got, "Go module location: 1 checked, 1 skipped") || !strings.Contains(got, "README.md:4: go vet ./... (unresolved working directory)") {
		t.Errorf("Go counts and skipped location missing from report:\n%s", got)
	}
	if got := strings.Join(report, "\n"); !strings.Contains(got, "Helm render: 0 checked, 1 skipped") || !strings.Contains(got, "skipped (Helm render): README.md:5:") {
		t.Errorf("Helm counts and skipped location missing from report:\n%s", got)
	}
	if got := strings.Join(report, "\n"); !strings.Contains(got, "SQL SELECT: 0 checked, 1 skipped") || !strings.Contains(got, "skipped (SQL SELECT): README.md:6:") || strings.Contains(got, "psql=1") {
		t.Errorf("SQL counts and skipped location missing from report:\n%s", got)
	}
	if err := os.Remove(readme); err != nil {
		t.Fatal(err)
	}

	// A tree whose blocks the extractor cannot see trips the floor instead of passing.
	zh := filepath.Join(repo, locales[2], "page.md")
	if err := os.WriteFile(zh, []byte(strings.Replace(page.String(), "```bash", "```", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	problems, _, _ = checkPublishedDocs(rootCmd, repo)
	if len(problems) != 2 || !strings.Contains(strings.Join(problems, "\n"), "zh-CN/docusaurus-plugin-content-docs/current: found only 0 dcctl") {
		t.Errorf("an unreadable tree did not trip both floors: %v", problems)
	}

	// A missing translation tree.
	if err := os.RemoveAll(filepath.Join(repo, "docs", "i18n", "zh-CN")); err != nil {
		t.Fatal(err)
	}
	problems, _, _ = checkPublishedDocs(rootCmd, repo)
	if len(problems) == 0 || !strings.Contains(problems[0], "found 2 docs trees") {
		t.Errorf("a missing translation tree was not reported: %v", problems)
	}
}

// scanOne runs the gate over a synthetic page.
func scanOne(t *testing.T, md string, paths ...string) *docsScan {
	t.Helper()
	rootCmd.InitDefaultHelpCmd()
	rootCmd.InitDefaultCompletionCmd()
	known := map[string]bool{}
	for _, p := range paths {
		known[p] = true
	}
	s := newDocsScan()
	scanDocsText(rootCmd, "page.md", md, func(p string) bool { return known[p] }, s)
	return s
}

func findingTexts(s *docsScan) string {
	var out []string
	for _, f := range s.findings {
		out = append(out, f.where+" `"+f.text+"`: "+f.msg)
	}
	return strings.Join(out, "\n")
}

func TestDocsGateGoModuleLocations(t *testing.T) {
	repo := t.TempDir()
	// One workspace root and a separate on-disk module exercise both sources.
	for name, content := range map[string]string{
		"go.work":                     "go 1.27.0\nuse (\n  ./backend/core // workspace module\n)\nuse \"./backend/cli\"\n",
		"backend/core/go.mod":         "module fixture/core\n",
		"backend/cli/go.mod":          "module fixture/cli\n",
		"examples/standalone/go.mod":  "module fixture/standalone\n",
		"backend/core/sub/package.go": "package sub\n",
	} {
		p := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	layout, err := readDocsGoLayout(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !layout.roots["backend/core"] || !layout.roots["backend/cli"] {
		t.Fatalf("workspace use entries lost: %v", layout.roots)
	}
	cases := []struct {
		name, shell                string
		checked, skipped, findings int
		want                       string
	}{
		{"repo root", "go build ./...", 1, 0, 1, "from . start outside a Go module"},
		{"backend prefix", "go vet ./backend/...", 1, 0, 1, "./backend/..."},
		{"module patterns", "go build ./backend/core/... ./backend/cli/...", 1, 0, 0, ""},
		{"mixed patterns", "go vet ./backend/core/... ./backend/...", 1, 0, 1, "./backend/..."},
		{"unresolved does not hide invalid", "go vet ./missing/... ./backend/...", 0, 1, 1, "./backend/..."},
		{"literal cd", "cd backend/core\ngo build ./...\ngo vet ./sub/...", 2, 0, 0, ""},
		{"quoted cd and env", "cd -- 'backend/core' && GOWORK=off go vet ./...", 1, 0, 0, ""},
		{"relative cd", "cd backend/core; cd ../cli; go build ./...", 1, 0, 0, ""},
		{"module subdirectory", "cd backend/core/sub\ngo build ../...", 1, 0, 0, ""},
		{"standalone module", "cd examples/standalone && go vet ./...", 1, 0, 0, ""},
		{"leave module", "cd backend/core; cd ..; go vet ./...", 1, 0, 1, "from backend start outside"},
		{"go -C is skipped, not judged", "go build -C backend/core ./...", 0, 1, 0, ""},
		{"cd dash", "cd backend/core; cd ../cli; cd -; go vet ./...", 1, 0, 0, ""},
		{"cd dash root", "cd backend/core; cd -; go build ./...", 1, 0, 1, "from . start outside"},
		{"subshell", "(cd backend/core && go build ./...); go vet ./...", 2, 0, 1, "from . start outside"},
		{"substitution isolated", "x=$(cd backend/core && go build ./...); go vet ./...", 1, 1, 1, "unresolved working directory"},
		{"substitution after cd", "cd backend/core && x=$(go build ./...)", 0, 1, 0, "unresolved working directory"},
		{"block reset", "cd backend/core\ngo build ./...\n```\n```bash\ngo vet ./...", 2, 0, 1, "from . start outside"},
		{"missing cd", "cd missing\ngo build ./...", 0, 1, 0, "unresolved working directory"},
		{"variable cd", "cd \"$module\" && go vet ./...", 0, 1, 0, "unresolved working directory"},
		{"substitution cd", "cd \"$(pwd)\"; go build ./...", 0, 1, 0, "unresolved working directory"},
		{"home cd", "cd; go vet ./...", 0, 1, 0, "unresolved working directory"},
		{"absolute cd", "cd /tmp; go build ./...", 0, 1, 0, "unresolved working directory"},
		{"escape repo", "cd ..; go vet ./...", 0, 1, 0, "unresolved working directory"},
		{"missing target", "go build ./missing/...", 0, 1, 0, "unresolved pattern directory"},
		{"dynamic pattern", "go vet ./backend/$module/...", 0, 1, 0, "dynamic package pattern"},
		{"loop", "for m in $(go list -m); do\n(cd \"$m\" && go build ./... && go vet ./...)\ndone", 0, 2, 0, "dynamic loop"},
		{"literal loop is beyond scope", "for m in backend/core; do cd backend/core; go vet ./...; done", 0, 1, 0, "dynamic loop"},
		{"not recursive build or vet", "go test ./...\ngo build ./cmd/tool\ngo vet all\ngo build example.com/pkg/...", 0, 0, 0, ""},
		{"comment and heredoc", "# go build ./...\ncat <<EOF\ngo vet ./...\nEOF", 0, 0, 0, ""},
		{"continuation", "cd backend/core && go build \\\n ./...", 1, 0, 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newDocsScan()
			s.goLayout = layout
			scanDocsText(rootCmd, "fixture.md", "```bash\n"+c.shell+"\n```\n", func(string) bool { return true }, s)
			if s.goChecked != c.checked || len(s.goSkipped) != c.skipped || len(s.findings) != c.findings {
				t.Fatalf("checked/skipped/findings = %d/%d/%d, want %d/%d/%d; findings: %s; skipped: %v",
					s.goChecked, len(s.goSkipped), len(s.findings), c.checked, c.skipped, c.findings, findingTexts(s), s.goSkipped)
			}
			got := findingTexts(s) + strings.Join(s.goSkipped, "\n")
			if c.want != "" && (!strings.Contains(got, c.want) || !strings.Contains(got, "fixture.md:")) {
				t.Errorf("want location and %q, got %s", c.want, got)
			}
		})
	}
}

// TestDocsGateNamesABogusCommand is the negative control: each planted defect must be
// reported, by line, with the reason.
func TestDocsGateNamesABogusCommand(t *testing.T) {
	cases := []struct {
		name, md, want string
	}{
		{"unknown command", "```bash\ndcctl frobnicate --nope\n```\n", `unknown command "frobnicate" for "dcctl"`},
		{"unknown flag", "```bash\ndcctl bootstrap local prod --nope\n```\n", "unknown flag: --nope"},
		{"unknown shorthand", "```bash\ndcctl bootstrap local prod -Q\n```\n", "unknown shorthand flag: 'Q'"},
		{"too few args", "```bash\ndcctl upgrade local\n```\n", "accepts 2 arg(s), received 1"},
		{"too many args", "```bash\ndcctl bootstrap local prod extra\n```\n", "accepts 2 arg(s), received 3"},
		{"unknown subcommand", "```bash\ndcctl sim frobnicate\n```\n", `unknown command "frobnicate" for "dcctl sim"`},
		{"after a continuation", "```bash\ndcctl bootstrap local prod \\\n  --nope\n```\n", "unknown flag: --nope"},
		{"in a substitution", "```bash\nTOKEN=\"$(dcctl frobnicate)\"\n```\n", `unknown command "frobnicate"`},
		{"after an env prefix and &&", "```sh\ncd x && FOO=1 ./bin/dcctl frobnicate\n```\n", `unknown command "frobnicate"`},
		{"in a pipe", "```bash\ndcctl frobnicate | jq .\n```\n", `unknown command "frobnicate"`},
		{"prompted console", "```console\n$ dcctl frobnicate\nsome output\n```\n", `unknown command "frobnicate"`},
		{"indented fence", "1. step\n\n   ```bash\n   dcctl frobnicate\n   ```\n", `unknown command "frobnicate"`},
		{"missing hack path", "```bash\n./hack/no-such-script.sh --self-test\n```\n", "no such path"},
		{"missing hack path after bash", "```bash\nbash hack/no-such-script.sh\n```\n", "no such path"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := scanOne(t, c.md)
			got := findingTexts(s)
			if !strings.Contains(got, c.want) {
				t.Errorf("planted defect not reported as %q; got:\n%s", c.want, got)
			}
			if !strings.Contains(got, "page.md:") {
				t.Errorf("finding does not name its line: %s", got)
			}
		})
	}
}

// TestDocsGatePassesWhatIsReal is the counterweight: a gate that rejects everything
// also "catches" the bogus command. Real commands, non-shell fences, comments, output,
// heredoc data and templates must all pass.
func TestDocsGatePassesWhatIsReal(t *testing.T) {
	md := strings.Join([]string{
		"```bash",
		"# dcctl frobnicate is mentioned in a comment",
		"dcctl bootstrap local prod --yes   # trailing dcctl frobnicate comment",
		"DCCTL_ESCROW_PASSPHRASE=x dcctl bootstrap local scratch --dev",
		"dcctl upgrade local prod 2>&1 | tee upgrade.log",
		"dcctl bootstrap --help",
		"dcctl <command> --help",
		"dcctl bootstrap <provider> <instance>",
		"dcctl sim",
		"cat <<EOF > notes.txt",
		"dcctl frobnicate inside a heredoc is data",
		"EOF",
		"curl -d '{",
		`  "note": "x"; dcctl frobnicate inside a multi-line quoted body is data`,
		"}' http://localhost:8080/graphql",
		"./hack/real.sh --self-test",
		"kubectl get pods -n dci-prod",
		"```",
		"",
		"```",
		"dcctl stopped waiting for the interrupted command",
		"```",
		"",
		"```text",
		"dcctl frobnicate",
		"```",
		"",
		"```console",
		"$ dcctl version",
		"dcctl frobnicate (this is output)",
		"```",
	}, "\n")
	s := scanOne(t, md, "hack/real.sh")
	if len(s.findings) != 0 {
		t.Fatalf("real commands were rejected:\n%s", findingTexts(s))
	}
	if s.dcctlChecked != 7 {
		t.Errorf("checked %d dcctl invocations, want 7 (a non-shell fence or comment was read, or a real one was lost)", s.dcctlChecked)
	}
	if len(s.dcctlSkipped) != 1 {
		t.Errorf("skipped %v, want only the `dcctl <command> --help` template", s.dcctlSkipped)
	}
	if s.hackChecked != 1 || s.uncovered["kubectl"] != 1 {
		t.Errorf("hack paths checked %d (want 1), kubectl counted %d (want 1)", s.hackChecked, s.uncovered["kubectl"])
	}
}

// TestDocsGateDoesNotTouchRealFlags: parsing the docs' arguments must not write into
// the command's real flag variables, which other tests and the binary read.
func TestDocsGateDoesNotTouchRealFlags(t *testing.T) {
	c, _, err := rootCmd.Find([]string{"bootstrap"})
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	c.Flags().VisitAll(func(f *pflag.Flag) { before[f.Name] = f.Value.String() })
	var all []string
	c.Flags().VisitAll(func(f *pflag.Flag) {
		if f.NoOptDefVal != "" {
			all = append(all, "--"+f.Name)
		} else {
			all = append(all, "--"+f.Name+"=planted")
		}
	})
	if len(all) == 0 {
		t.Fatal("precondition: bootstrap has no flags; the check below proves nothing")
	}
	if p, _ := checkDcctl(rootCmd, append([]string{"bootstrap", "local", "prod"}, all...)); p != "" {
		t.Fatalf("every real bootstrap flag was rejected: %s", p)
	}
	c.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Value.String() != before[f.Name] || f.Changed {
			t.Errorf("--%s was written by the gate: %q -> %q", f.Name, before[f.Name], f.Value.String())
		}
	})
}

// This regression uses only the pre-existing scanner entry point and fields so
// it also compiles on main, where Helm is merely counted as uncovered.
func TestDocsHelmMissingRootKey(t *testing.T) {
	s := scanOne(t, "```bash\nhelm install dc deploy/helm/devicechain --set instance.id=devicechain\n```\n")
	if len(s.findings) != 1 || !strings.Contains(findingTexts(s), "instance.config.infrastructure.secrets.rootKey is required") || !strings.Contains(findingTexts(s), "page.md:2") {
		t.Fatalf("missing root key must produce one located render finding; got %s; uncovered: %v", findingTexts(s), s.uncovered)
	}
}

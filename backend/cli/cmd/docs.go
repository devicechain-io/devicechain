// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

// The generated command reference is COMMITTED, not built with the docs site: the docs
// build has no Go toolchain, and a page that cannot be regenerated there must not be
// able to drift here. TestCommittedReferenceIsCurrent regenerates it and diffs.

// docsPage is where each locale's copy of the reference lives, relative to the docs
// root, and the front matter + notice that precede the shared generated body.
type docsPage struct {
	path   string
	header string
}

// referenceBodyIntro is the explicit id on the page heading, so a link into the page
// does not depend on a translated title.
const referenceBodyIntro = "{#dcctl-reference}"

var referencePages = []docsPage{
	{
		path: "docs/reference/dcctl.md",
		header: "---\nsidebar_position: 4\ntitle: dcctl Command Reference\n---\n\n" +
			"# dcctl Command Reference " + referenceBodyIntro + "\n\n" +
			"Every `dcctl` command, with its flags and defaults. This page is generated from the " +
			"command definitions in the binary, so it cannot fall behind them; run `dcctl <command> --help` " +
			"for the same text at a terminal.\n\n",
	},
	{
		path: "i18n/es/docusaurus-plugin-content-docs/current/reference/dcctl.md",
		header: "---\nsidebar_position: 4\ntitle: Referencia de comandos de dcctl\n---\n\n" +
			"# Referencia de comandos de dcctl " + referenceBodyIntro + "\n\n" +
			"Todos los comandos de `dcctl`, con sus opciones y valores predeterminados. Esta página se genera " +
			"a partir de las definiciones de comandos del binario, así que no puede quedarse atrás; ejecuta " +
			"`dcctl <comando> --help` para ver el mismo texto en un terminal.\n\n" +
			":::note\nLa referencia de comandos y opciones que sigue se genera y está solo en inglés.\n:::\n\n",
	},
	{
		path: "i18n/zh-CN/docusaurus-plugin-content-docs/current/reference/dcctl.md",
		header: "---\nsidebar_position: 4\ntitle: dcctl 命令参考\n---\n\n" +
			"# dcctl 命令参考 " + referenceBodyIntro + "\n\n" +
			"列出所有 `dcctl` 命令及其标志和默认值。本页由二进制文件中的命令定义自动生成，因此不会落后于实现；" +
			"在终端中运行 `dcctl <命令> --help` 可看到相同的文本。\n\n" +
			":::note\n下面的命令和标志参考为自动生成，仅提供英文版本。\n:::\n\n",
	},
}

// referenceAnchor is the heading id of a command's section.
func referenceAnchor(c *cobra.Command) string {
	return strings.ReplaceAll(c.CommandPath(), " ", "-")
}

// documented reports whether a command belongs on the public page. Cobra adds `help`
// (and, once a command has executed, `completion`) itself; neither is DeviceChain's
// to document and their presence would depend on whether anything ran first.
func documented(c *cobra.Command) bool {
	if c.Hidden || c.Deprecated != "" {
		return false
	}
	if c.Parent() == rootCmd && (c.Name() == "help" || c.Name() == "completion") {
		return false
	}
	return true
}

func collectDocumented(c *cobra.Command, out *[]*cobra.Command) {
	for _, child := range c.Commands() {
		if !documented(child) {
			continue
		}
		*out = append(*out, child)
		collectDocumented(child, out)
	}
}

// escapeMDX makes help text safe for a Docusaurus page, which parses .md as MDX: a bare
// `<instance>` is an unclosed JSX tag, a bare `{` opens an expression, and either one
// fails the whole site build. MDX also has no indented code blocks, so a run of indented
// lines (the examples and aligned tables in long help) would reflow into a paragraph or,
// for a `#` line, a heading: each such run is wrapped in a fence instead. Existing fences
// (backtick or tilde, any length) and inline code spans are literal and left alone;
// everything else gets `<`, `{`, `}` and `&` backslash-escaped.
func escapeMDX(text string) string {
	lines := strings.Split(text, "\n")
	var out []string
	var fenceCh byte
	fenceLen := 0
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimLeft(line, " ")
		if fenceLen > 0 {
			out = append(out, line)
			if ch, n := fenceRun(trimmed); ch == fenceCh && n >= fenceLen && strings.TrimSpace(trimmed[n:]) == "" {
				fenceLen = 0
			}
			continue
		}
		if ch, n := fenceRun(trimmed); n >= 3 {
			fenceCh, fenceLen = ch, n
			out = append(out, line)
			continue
		}
		if isIndented(line) {
			// Take the whole run, including blank lines that are followed by more
			// indented text, and fence it verbatim.
			j := i
			for k := i; k < len(lines); k++ {
				if isIndented(lines[k]) {
					j = k
				} else if strings.TrimSpace(lines[k]) != "" {
					break
				}
			}
			out = append(out, "```")
			out = append(out, lines[i:j+1]...)
			out = append(out, "```")
			i = j
			continue
		}
		out = append(out, escapeInline(line))
	}
	return strings.Join(out, "\n")
}

// fenceRun reports the fence character and length a line opens with (0 if none).
func fenceRun(s string) (byte, int) {
	if s == "" || (s[0] != '`' && s[0] != '~') {
		return 0, 0
	}
	n := 0
	for n < len(s) && s[n] == s[0] {
		n++
	}
	if n < 3 {
		return 0, 0
	}
	return s[0], n
}

func isIndented(line string) bool {
	return strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t")
}

// escapeInline escapes one line of prose, skipping inline code spans. A span opens at a
// run of N backticks and closes at the next run of exactly N; a run with no closer is
// literal text, as in CommonMark.
func escapeInline(line string) string {
	var b strings.Builder
	for i := 0; i < len(line); {
		if line[i] != '`' {
			j := strings.IndexByte(line[i:], '`')
			if j < 0 {
				j = len(line) - i
			}
			b.WriteString(mdxEscaper.Replace(line[i : i+j]))
			i += j
			continue
		}
		n := 0
		for i+n < len(line) && line[i+n] == '`' {
			n++
		}
		end := closingRun(line, i+n, n)
		if end < 0 {
			b.WriteString(line[i : i+n])
			i += n
			continue
		}
		b.WriteString(line[i : end+n])
		i = end + n
	}
	return b.String()
}

// closingRun returns the index of the next backtick run of exactly n, or -1.
func closingRun(line string, from, n int) int {
	for i := from; i < len(line); {
		if line[i] != '`' {
			i++
			continue
		}
		m := 0
		for i+m < len(line) && line[i+m] == '`' {
			m++
		}
		if m == n {
			return i
		}
		i += m
	}
	return -1
}

var mdxEscaper = strings.NewReplacer("<", `\<`, "{", `\{`, "}", `\}`, "&", `\&`)

// renderReferenceBody renders every documented command as one markdown body. The
// body is identical in every locale: it is the binary's own English text.
func renderReferenceBody() ([]byte, error) {
	// The banner in the root command's Long text is colored for a terminal; the page
	// must render the same whether or not the generator ran on one.
	prev := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = prev }()

	var cmds []*cobra.Command
	collectDocumented(rootCmd, &cmds)

	var buf bytes.Buffer
	// The root gets a hand-written section: its Long text is an ASCII-art banner, which
	// markdown would reflow into noise.
	fmt.Fprintf(&buf, "## dcctl {#%s}\n\n%s.\n\n```\ndcctl [command]\n```\n\n", referenceAnchor(rootCmd), escapeMDX(rootCmd.Short))
	buf.WriteString("### Commands\n\n")
	for _, c := range rootCmd.Commands() {
		if documented(c) {
			fmt.Fprintf(&buf, "* [%s](#%s)\t - %s\n", c.CommandPath(), referenceAnchor(c), escapeMDX(c.Short))
		}
	}
	buf.WriteString("\n")

	link := func(name string) string {
		return "#" + strings.ReplaceAll(strings.TrimSuffix(name, ".md"), "_", "-")
	}
	for _, c := range cmds {
		var section bytes.Buffer
		// cobra's flag-usage rendering reads nothing from the environment, but its
		// auto-generated footer carries a date: it is switched off.
		c.DisableAutoGenTag = true
		if err := doc.GenMarkdownCustom(c, &section, link); err != nil {
			return nil, fmt.Errorf("rendering %q: %w", c.CommandPath(), err)
		}
		heading := "## " + c.CommandPath() + "\n"
		body := section.String()
		if !strings.HasPrefix(body, heading) {
			return nil, fmt.Errorf("cobra no longer opens %q with %q; the reference's heading anchors need revisiting", c.CommandPath(), heading)
		}
		body = "## " + c.CommandPath() + " {#" + referenceAnchor(c) + "}\n" + escapeMDX(strings.TrimPrefix(body, heading))
		buf.WriteString(body)
		buf.WriteString("\n")
	}
	// Collapse the doubled blank lines cobra leaves between sections.
	out := buf.String()
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	return []byte(strings.TrimRight(out, "\n") + "\n"), nil
}

// renderReferencePages returns each locale's complete page, keyed by its path relative
// to the docs root.
func renderReferencePages() (map[string][]byte, error) {
	body, err := renderReferenceBody()
	if err != nil {
		return nil, err
	}
	pages := make(map[string][]byte, len(referencePages))
	for _, p := range referencePages {
		pages[p.path] = append([]byte(p.header), body...)
	}
	return pages, nil
}

var docsOut string

// docsCmd regenerates the committed command reference. It is hidden: it is a
// maintainer tool, and listing it on the page it generates would be circular.
var docsCmd = &cobra.Command{
	Use:    "docs",
	Short:  "Regenerate the published command reference",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		pages, err := renderReferencePages()
		if err != nil {
			return err
		}
		for rel, content := range pages {
			dst := filepath.Join(docsOut, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(dst, content, 0o644); err != nil {
				return err
			}
			fmt.Println(dst)
		}
		return nil
	},
	SilenceUsage: true,
}

func init() {
	docsCmd.Flags().StringVar(&docsOut, "out", "", "docs root to write the three locale pages under (the repository's docs/ directory)")
	_ = docsCmd.MarkFlagRequired("out")
	rootCmd.AddCommand(docsCmd)
}

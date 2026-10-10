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
// `<instance>` is an unclosed JSX tag and a bare `{` opens an expression, and either one
// fails the whole site build. Fenced blocks and inline code are literal and are left
// alone; everything else gets the three characters backslash-escaped.
func escapeMDX(text string) string {
	lines := strings.Split(text, "\n")
	inFence := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		parts := strings.Split(line, "`")
		for j := 0; j < len(parts); j += 2 { // even segments are outside inline code
			parts[j] = mdxEscaper.Replace(parts[j])
		}
		lines[i] = strings.Join(parts, "`")
	}
	return strings.Join(lines, "\n")
}

var mdxEscaper = strings.NewReplacer("<", `\<`, "{", `\{`, "}", `\}`)

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

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/key"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// InstanceFacts is what the command layer read back after a bootstrap run: the
// facts the success screen and the receipt state, none of them secret.
type InstanceFacts struct {
	// ConsoleURL is the admin console's address (bootstrap.ConsoleURL).
	ConsoleURL string
	// Account is the superuser's sign-in name.
	Account string
	// KubeContext is the context the instance was built through.
	KubeContext string
	// Backups describes the instance's backups, as the cluster was installed.
	Backups string
	// EscrowPath is where the root-key escrow artifact is, when the run settled
	// one; EscrowWritten says this run wrote it and checked it back.
	EscrowPath    string
	EscrowWritten bool
}

// passphraseFilePlaceholder stands in for the passphrase file in the equivalent
// command: the command is a template, and the passphrase is never in it.
const passphraseFilePlaceholder = "<file holding the escrow passphrase>"

// BootstrapCommand is the equivalent non-interactive command for the choices, as
// a POSIX template: --yes reproduces the approval, and the passphrase is named
// only as a placeholder for a protected file. targetArgs are the target's flags
// (--kube-context or --cluster) as the command line had them.
func BootstrapCommand(in InstanceChoices, esc EscrowChoice, targetArgs []string) string {
	return BootstrapCommandOf(BootstrapArgs(in, esc, targetArgs), esc)
}

// BootstrapCommandOf is BootstrapCommand for a command line already composed, word by
// word and unquoted (BootstrapArgs, as the command layer may have adjusted it).
func BootstrapCommandOf(words []string, esc EscrowChoice) string {
	argv := quoteArgs(words)
	if esc.Mode != EscrowNone {
		argv = append(argv, "--escrow-passphrase-file="+passphraseFilePlaceholder)
	}
	return strings.Join(append(argv, "--yes"), " ")
}

// BootstrapContinueCommand is the command that builds the chosen instance later, by
// hand, as a POSIX command line: the same flags as BootstrapCommand, but with no
// --yes and no passphrase file, so the bootstrap asks for the passphrase at the
// terminal, as a person running it expects, and with --ui=tui, so it opens on these
// same screens, the choices seeded from its flags.
func BootstrapContinueCommand(in InstanceChoices, esc EscrowChoice, targetArgs []string) string {
	return strings.Join(append(quoteArgs(BootstrapArgs(in, esc, targetArgs)), "--ui=tui"), " ")
}

// BootstrapArgs is the bootstrap command line the choices describe, word by word
// and unquoted: "dcctl", "bootstrap", the provider, the instance name, the target's
// flags, then every flag the choices carry, ending with the escrow's. It names no
// passphrase and gives no approval. dcctl setup local parses exactly these words
// through the bootstrap command's own flags, so the run it starts is the one this
// command line describes.
func BootstrapArgs(in InstanceChoices, esc EscrowChoice, targetArgs []string) []string {
	argv := append([]string{"dcctl", "bootstrap", "local", in.Name}, targetArgs...)
	flag := func(name, value string) {
		if in.Given[name] {
			argv = append(argv, "--"+name+"="+value)
		}
	}
	flag("profile", in.Profile)
	if in.Given["enable-area"] {
		for _, a := range in.Areas {
			argv = append(argv, "--enable-area="+a)
		}
	}
	flag("host", in.Host)
	if in.Given["no-tls"] {
		argv = append(argv, fmt.Sprintf("--no-tls=%t", in.NoTLS))
	}
	flag("object-store", in.ObjectStore)
	switch esc.Mode {
	case EscrowNone:
		argv = append(argv, "--no-escrow")
	case EscrowRestore:
		argv = append(argv, "--restore-root-key="+esc.Path)
	default:
		if esc.PathGiven {
			argv = append(argv, "--escrow-file="+esc.Path)
		}
	}
	return argv
}

// quoteArgs quotes each word for a POSIX shell.
func quoteArgs(argv []string) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = shellQuote(a)
	}
	return out
}

// successModel is screen S8, the instance success.
type successModel struct {
	instance InstanceChoices
	escrow   EscrowChoice
	facts    InstanceFacts
	seedRef  *installflow.SecretRef
	seenOnce bool // the seed was shown on the first-login screen
	acked    bool // off-site custody confirmed on the receipt
	target   []string
}

var bsKeyExit = key.NewBinding(key.WithKeys("enter", "q"), key.WithHelp("Enter/q", "exit"))

func (m *successModel) passwordLine() string {
	switch {
	case m.seedRef == nil:
		return "unchanged by this run"
	case m.seenOnce:
		return "shown once on the previous screen; also kept in Secret " + refText(*m.seedRef)
	}
	return "kept in Secret " + refText(*m.seedRef)
}

func refText(r installflow.SecretRef) string {
	s := r.Namespace + "/" + r.Name
	if r.Key != "" {
		s += ", key " + r.Key
	}
	return s
}

func (m *successModel) escrowLine() string {
	switch m.escrow.Mode {
	case EscrowNone:
		return "none (--no-escrow): losing the cluster loses every stored secret"
	case EscrowRestore:
		return "root key restored from " + m.escrow.Path
	}
	path := m.facts.EscrowPath
	if path == "" {
		path = m.escrow.Path
	}
	if m.acked {
		return path + " (off-site custody confirmed)"
	}
	return path + " (off-site custody not confirmed)"
}

func (m *successModel) profileLine() string {
	p := m.instance.Profile
	if len(m.instance.Areas) > 0 {
		p += " + " + strings.Join(m.instance.Areas, ", ")
	}
	return p
}

func (m *successModel) screen(t bsTheme) bsScreen {
	w := t.contentWidth()
	const lw = 12
	var body []string
	body = append(body, t.para(roleGood, "", "Instance "+m.instance.Name+" is ready.", w)...)
	body = append(body, "")
	body = append(body, t.field("Console", orDefault(m.facts.ConsoleURL, "(not reported)"), lw, w)...)
	body = append(body, t.field("Account", orDefault(m.facts.Account, "(not reported)"), lw, w)...)
	body = append(body, t.field("Password", m.passwordLine(), lw, w)...)
	body = append(body, t.field("Profile", m.profileLine(), lw, w)...)
	body = append(body, t.field("Backups", orDefault(m.facts.Backups, "as the cluster was installed"), lw, w)...)
	body = append(body, t.field("Escrow", m.escrowLine(), lw, w)...)
	body = append(body, "")
	body = append(body, t.paint(roleLabel, "Next"))
	body = append(body, t.para(rolePlain, "  ", "Open the console, sign in as "+orDefault(m.facts.Account, "the superuser")+
		", create your first tenant, and change the password there.", w)...)
	if m.seedRef != nil && !m.seenOnce {
		body = append(body, t.para(rolePlain, "  ", "Read the password: kubectl -n "+m.seedRef.Namespace+" get secret "+
			m.seedRef.Name+" -o jsonpath='{.data."+m.seedRef.Key+"}' | base64 -d", w)...)
	}
	body = append(body, "")
	body = append(body, t.paint(roleLabel, "Equivalent command (template)"))
	body = append(body, t.para(rolePlain, "  ", BootstrapCommand(m.instance, m.escrow, m.target), w)...)
	return bsScreen{title: "Instance ready", body: body, keys: []key.Binding{bsKeyExit, bsKeyHelp}}
}

// exitSummary is the short, durable record left on stdout once the terminal is
// restored. It holds no secret: the password is named only by where it is kept.
func (m *successModel) exitSummary(w io.Writer) {
	fmt.Fprintf(w, "Instance %s is ready.\n", m.instance.Name)
	fmt.Fprintf(w, "  Console:  %s\n", orDefault(m.facts.ConsoleURL, "(not reported)"))
	fmt.Fprintf(w, "  Account:  %s\n", orDefault(m.facts.Account, "(not reported)"))
	if m.seedRef != nil {
		fmt.Fprintf(w, "  Password: kept in Secret %s\n", refText(*m.seedRef))
	}
	fmt.Fprintf(w, "  Escrow:   %s\n", m.escrowLine())
	fmt.Fprintf(w, "  Command (template): %s\n", BootstrapCommand(m.instance, m.escrow, m.target))
}

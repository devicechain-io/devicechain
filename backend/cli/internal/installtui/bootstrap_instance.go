// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// The functional profiles a bootstrap offers, in the order the screen lists them.
var bootstrapProfiles = []string{"default", "full", "telemetry", "ingest-only"}

// The object stores an instance can be given on this screen. "auto" is the flag
// omitted: a filesystem store on a cluster dcctl created, none on one it was
// pointed at. An S3 store needs a bucket and a credentials reference, which only
// flags supply; one given that way is carried, read-only.
var bootstrapObjectStores = []string{"auto", "none", "filesystem"}

// InstanceSeed is what the instance-choices screen starts from: the command line
// as it was parsed, and the checks the command layer owns.
type InstanceSeed struct {
	Name, Host, Profile, ObjectStore string
	Areas                            []string
	NoTLS                            bool
	// FromFlag names each flag the command line supplied, by its flag name
	// ("profile", "host", "enable-area", "no-tls", "object-store"; "name" for the
	// positional instance name). Those values are labelled "from --flag", and the
	// name "from the command line".
	FromFlag map[string]bool
	// ClusterShape describes the cluster the instance inherits (footprint and
	// topology). It is shown and never edited here.
	ClusterShape string
	// ValidateName is the command's instance-name rule; ValidateAreas its area
	// catalogue check. Nil accepts anything.
	ValidateName  func(string) error
	ValidateAreas func([]string) error
}

// InstanceChoices is the instance the person settled on, with whether each
// value is to be passed as a flag.
type InstanceChoices struct {
	Name, Host, Profile, ObjectStore string
	Areas                            []string
	NoTLS                            bool
	// Given names the flags the equivalent command carries: those the command
	// line supplied, and those changed on the screen.
	Given map[string]bool
}

// InstanceChosen is sent when the person continues from the instance screen.
type InstanceChosen struct{ Choices InstanceChoices }

// ScreenBack is sent when the person steps back from the first bootstrap screen.
type ScreenBack struct{}

type instanceField int

const (
	fieldName instanceField = iota
	fieldHost
	fieldProfile
	fieldAreas
	fieldTLS
	fieldObjectStore
	fieldBack
	fieldContinue
	instanceFieldCount
)

// instanceModel is screen S3b, the instance choices.
type instanceModel struct {
	seed    InstanceSeed
	name    textinput.Model
	host    textinput.Model
	areas   textinput.Model
	profile int
	store   int
	noTLS   bool
	focus   instanceField
	errs    map[instanceField]string
	s3      bool // an S3 store came from the flags: carried, not offered
}

func newTextField(value string) textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.KeyMap.Paste.SetEnabled(false) // a terminal paste arrives as a paste event; never read the clipboard
	ti.SetValue(value)
	return ti
}

func newInstanceModel(seed InstanceSeed) *instanceModel {
	m := &instanceModel{seed: seed, noTLS: seed.NoTLS, errs: map[instanceField]string{}}
	m.name = newTextField(seed.Name)
	m.host = newTextField(seed.Host)
	m.areas = newTextField(strings.Join(seed.Areas, ","))
	m.profile = indexOr(bootstrapProfiles, seed.Profile, 0)
	m.s3 = seed.ObjectStore == "s3"
	m.store = indexOr(bootstrapObjectStores, seed.ObjectStore, 0)
	m.setFocus(fieldName)
	return m
}

func indexOr(list []string, v string, def int) int {
	for i, s := range list {
		if s == v {
			return i
		}
	}
	return def
}

func (m *instanceModel) input(f instanceField) *textinput.Model {
	switch f {
	case fieldName:
		return &m.name
	case fieldHost:
		return &m.host
	case fieldAreas:
		return &m.areas
	}
	return nil
}

func (m *instanceModel) setFocus(f instanceField) {
	if in := m.input(m.focus); in != nil {
		in.Blur()
	}
	if f == fieldObjectStore && m.s3 {
		f = fieldBack // a carried S3 store is not a stop
	}
	m.focus = f
	if in := m.input(f); in != nil {
		_ = in.Focus() // the returned blink command is not used: the caret is drawn statically
	}
}

func (m *instanceModel) move(d int) {
	f := (int(m.focus) + d + int(instanceFieldCount)) % int(instanceFieldCount)
	if instanceField(f) == fieldObjectStore && m.s3 {
		f = (f + d + int(instanceFieldCount)) % int(instanceFieldCount)
	}
	m.setFocus(instanceField(f))
}

var (
	bsKeyNext   = key.NewBinding(key.WithKeys("tab"), key.WithHelp("Tab", "next"))
	bsKeyPrev   = key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("Shift+Tab", "previous"))
	bsKeyChoose = key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("Up/Down", "choose"))
	bsKeyToggle = key.NewBinding(key.WithKeys("space", " "), key.WithHelp("Space", "toggle"))
	bsKeyEnter  = key.NewBinding(key.WithKeys("enter"), key.WithHelp("Enter", "activate"))
	bsKeyBack   = key.NewBinding(key.WithKeys("esc"), key.WithHelp("Esc", "back"))
	bsKeyHelp   = key.NewBinding(key.WithKeys("f1", "?"), key.WithHelp("?/F1", "help"))
)

func (m *instanceModel) keys() []key.Binding {
	out := []key.Binding{bsKeyNext, bsKeyPrev}
	switch m.focus {
	case fieldProfile, fieldObjectStore:
		out = append(out, bsKeyChoose)
	case fieldTLS:
		out = append(out, bsKeyToggle)
	}
	return append(out, bsKeyEnter, bsKeyBack, bsKeyHelp)
}

func (m *instanceModel) update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		if p, ok := msg.(tea.PasteMsg); ok {
			if in := m.input(m.focus); in != nil {
				*in, _ = in.Update(p)
			}
		}
		return nil
	}
	switch k.String() {
	case "tab":
		m.move(1)
		return nil
	case "shift+tab":
		m.move(-1)
		return nil
	case "esc":
		return func() tea.Msg { return ScreenBack{} }
	case "enter":
		switch m.focus {
		case fieldBack:
			return func() tea.Msg { return ScreenBack{} }
		case fieldContinue:
			return m.submit()
		}
		m.move(1)
		return nil
	case "up", "left":
		m.cycle(-1)
	case "down", "right":
		m.cycle(1)
	case "space", " ":
		if m.focus == fieldTLS {
			m.noTLS = !m.noTLS
			return nil
		}
	}
	if in := m.input(m.focus); in != nil {
		*in, _ = in.Update(k)
		delete(m.errs, m.focus)
	}
	return nil
}

func (m *instanceModel) cycle(d int) {
	switch m.focus {
	case fieldProfile:
		m.profile = (m.profile + d + len(bootstrapProfiles)) % len(bootstrapProfiles)
	case fieldObjectStore:
		m.store = (m.store + d + len(bootstrapObjectStores)) % len(bootstrapObjectStores)
	}
}

// areaList is the comma-separated areas field, in the order typed.
func (m *instanceModel) areaList() []string {
	var out []string
	for _, a := range strings.Split(m.areas.Value(), ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// choices is what the screen holds now, with the flags it would pass.
func (m *instanceModel) choices() InstanceChoices {
	c := InstanceChoices{
		Name: strings.TrimSpace(m.name.Value()), Host: strings.TrimSpace(m.host.Value()),
		Profile: bootstrapProfiles[m.profile], Areas: m.areaList(), NoTLS: m.noTLS,
		ObjectStore: bootstrapObjectStores[m.store], Given: map[string]bool{},
	}
	if m.s3 {
		c.ObjectStore = "s3"
	}
	from := m.seed.FromFlag
	c.Given["name"] = true // the positional argument is always given
	c.Given["host"] = from["host"] || c.Host != m.seed.Host
	c.Given["profile"] = from["profile"] || c.Profile != orDefault(m.seed.Profile, "default")
	c.Given["enable-area"] = from["enable-area"] || strings.Join(c.Areas, ",") != strings.Join(m.seed.Areas, ",")
	c.Given["no-tls"] = from["no-tls"] || c.NoTLS != m.seed.NoTLS
	c.Given["object-store"] = from["object-store"] || c.ObjectStore != orDefault(m.seed.ObjectStore, "auto")
	if c.ObjectStore == "auto" {
		c.Given["object-store"] = false
	}
	return c
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// submit validates with the command's own rules; a refusal stays on the screen
// with the field focused and the reason beside it.
func (m *instanceModel) submit() tea.Cmd {
	c := m.choices()
	m.errs = map[instanceField]string{}
	if c.Name == "" {
		m.errs[fieldName] = "an instance name is required"
	} else if m.seed.ValidateName != nil {
		if err := m.seed.ValidateName(c.Name); err != nil {
			m.errs[fieldName] = err.Error()
		}
	}
	if m.seed.ValidateAreas != nil && len(c.Areas) > 0 {
		if err := m.seed.ValidateAreas(c.Areas); err != nil {
			m.errs[fieldAreas] = err.Error()
		}
	}
	for _, f := range []instanceField{fieldName, fieldAreas} {
		if _, bad := m.errs[f]; bad {
			m.setFocus(f)
			return nil
		}
	}
	return func() tea.Msg { return InstanceChosen{Choices: c} }
}

func (m *instanceModel) screen(t bsTheme) bsScreen {
	w := t.contentWidth()
	from := m.seed.FromFlag
	var body []string
	body = append(body, t.para(rolePlain, "", "Choose the instance to create on this cluster. "+
		"No changes have been made.", w)...)
	body = append(body, "")
	row := func(f instanceField, label, value, flag string) {
		mark, role := "  ", rolePlain
		if m.focus == f {
			mark, role = t.focusMark()+" ", roleFocus
		}
		switch {
		case from[flag] && flag == "name":
			value += "  (from the command line)" // the positional argument has no flag
		case from[flag]:
			value += "  (from --" + flag + ")"
		}
		for i, v := range wrap(value, w-16) {
			lead := strings.Repeat(" ", 16)
			if i == 0 {
				lead = mark + t.paint(roleLabel, padRight(label, 14))
			}
			body = append(body, lead+t.paint(role, v))
		}
		if e, ok := m.errs[f]; ok {
			body = append(body, t.para(roleBad, strings.Repeat(" ", 16), "ERROR: "+e, w)...)
		}
	}
	text := func(f instanceField, in textinput.Model, placeholder string) string {
		v := in.Value()
		if m.focus == f {
			pos := min(in.Position(), len([]rune(v)))
			r := []rune(v)
			return string(r[:pos]) + t.caret() + string(r[pos:])
		}
		if v == "" {
			return placeholder
		}
		return v
	}
	row(fieldName, "Name", text(fieldName, m.name, "(required)"), "name")
	row(fieldHost, "Host", text(fieldHost, m.host, "devicechain.local (default)"), "host")
	row(fieldProfile, "Profile", choiceText(t, bootstrapProfiles, m.profile), "profile")
	row(fieldAreas, "Extra areas", text(fieldAreas, m.areas, "none"), "enable-area")
	tls := t.check(!m.noTLS) + " serve HTTPS with a self-signed certificate"
	if m.noTLS {
		tls += " (off: --no-tls)"
	}
	row(fieldTLS, "TLS", tls, "no-tls")
	if m.s3 {
		body = append(body, "  "+t.paint(roleLabel, padRight("Object store", 14))+"s3, carried from the --object-store-s3-* flags")
	} else {
		row(fieldObjectStore, "Object store", choiceText(t, bootstrapObjectStores, m.store), "object-store")
	}
	if m.focus == fieldObjectStore && bootstrapObjectStores[m.store] == "auto" {
		body = append(body, t.para(roleDim, strings.Repeat(" ", 16),
			"auto: a small filesystem store on a cluster dcctl created, none on an existing context.", w)...)
	}
	if m.focus == fieldAreas {
		body = append(body, t.para(roleDim, strings.Repeat(" ", 16),
			"Areas deployed on top of the profile, separated by commas (--enable-area).", w)...)
	}
	body = append(body, "")
	body = append(body, t.field("Cluster", orDefault(m.seed.ClusterShape, "as installed")+"  (inherited, read-only)", 12, w)...)
	body = append(body, "", "  "+button(t, "Back", m.focus == fieldBack)+"   "+button(t, "Continue", m.focus == fieldContinue))
	return bsScreen{title: "Instance", body: body, keys: m.keys(), before: true}
}

func choiceText(t bsTheme, list []string, i int) string {
	return fmt.Sprintf("< %s >  (%d of %d)", list[i], i+1, len(list))
}

func button(t bsTheme, label string, focused bool) string {
	if focused {
		return t.focusMark() + t.paint(roleFocus, "["+label+"]")
	}
	return " [" + label + "]"
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s + " "
	}
	return s + strings.Repeat(" ", n-len(s))
}

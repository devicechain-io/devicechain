// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"io"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"
)

// Exercise the planned API surface and retain the exact v2 stack through tidy
// before screens land. teatest is deliberately imported only in a test file.
func TestDependencySurface(t *testing.T) {
	input := textinput.New()
	input.EchoMode = textinput.EchoNone
	input.SetValue("dependency-secret-canary")
	if strings.Contains(input.View(), "dependency-secret-canary") {
		t.Fatal("EchoNone exposed its value")
	}
	view := viewport.New(viewport.WithWidth(80), viewport.WithHeight(24))
	view.SetContent("dependency viewport")
	if !strings.Contains(view.View(), "dependency viewport") || lipgloss.Width(progress.New().ViewAs(0.5)) == 0 {
		t.Fatal("Bubbles did not render in memory")
	}
	model := teatest.NewTestModel(t, dependencyModel{}, teatest.WithInitialTermSize(80, 24))
	out, err := io.ReadAll(model.FinalOutput(t, teatest.WithFinalTimeout(3*time.Second)))
	if err != nil || !strings.Contains(string(out), "dependency loop") {
		t.Fatalf("in-memory Tea loop: %v: %q", err, out)
	}
}

type dependencyModel struct{}

func (dependencyModel) Init() tea.Cmd                         { return tea.Quit }
func (m dependencyModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (dependencyModel) View() tea.View                        { return tea.NewView("dependency loop") }

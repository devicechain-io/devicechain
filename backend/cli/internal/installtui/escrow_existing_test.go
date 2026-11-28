// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

func TestExistingEscrowPromptAsksForTheOriginalPassphrase(t *testing.T) {
	m := newPassphraseModel(SecretQuestion{Question: installflow.Question{
		Question: "bootstrap.escrow-existing-passphrase",
		Params: []installflow.Param{{Name: "artifact", Value: installflow.Text("/k/prod.escrow")}},
	}}, nil)
	screen := m.screen(bsTheme{width: 120})
	body := strings.Join(screen.body, "\n")
	if !strings.Contains(body, "Passphrase of the existing escrow at /k/prod.escrow") || strings.Contains(body, "Choose") {
		t.Fatalf("existing escrow asks for a new passphrase: %s", body)
	}
}

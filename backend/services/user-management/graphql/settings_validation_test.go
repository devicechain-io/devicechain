// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/settingsdefs"
	"github.com/stretchr/testify/require"
)

// 🔴 These exist because SetSetting no longer mentions any key by name. Shape
// validation moved into the key's own definition and runs inside Service.Set,
// which is better — it covers every write path instead of this one mutation —
// but it also means nothing in the resolver would look wrong if the wiring came
// apart. The settingsdefs tests prove each validator WORKS; these prove the
// mutation an operator actually calls still runs them.
//
// entity.token_masks is the one that matters most here: it is the key that had no
// validation at all, so it is the one where "the gate is wired up" is a new claim
// rather than a preserved one.

func setSetting(t *testing.T, key, value string) error {
	t.Helper()
	ctx, _ := newSettingsCtx(t, string(auth.SettingsWrite))
	r := &SettingsResolver{}
	_, err := r.SetSetting(ctx, struct {
		Key   string
		Value string
	}{Key: key, Value: value})
	return err
}

// A mask with a typo'd placeholder is valid JSON and used to be storable. It mints
// "dev-" for every device — a truncated token the operator would blame on the
// console.
func TestSetSettingRefusesAMaskThatMintsNothing(t *testing.T) {
	err := setSetting(t, settingsdefs.KeyTokenMasks, `{"device":"dev-{sulg}"}`)
	require.Error(t, err, "a mask with an unknown placeholder must be refused")
	require.Contains(t, err.Error(), "{sulg}")
}

// The positive control: without it, a validator that refused everything would pass
// the test above.
func TestSetSettingStoresValidTokenMasks(t *testing.T) {
	require.NoError(t, setSetting(t, settingsdefs.KeyTokenMasks,
		`{"default":"{slug}","device":"device-{alphanumeric-4}"}`))
}

// The third key, so the wiring is shown to cover the whole registry rather than
// whichever key a test happened to pick.
func TestSetSettingRefusesAnInvalidBrandingDefault(t *testing.T) {
	require.Error(t, setSetting(t, settingsdefs.KeyBrandingDefault, `{"primary":"blue"}`),
		"a non-hex color must be refused")
	require.NoError(t, setSetting(t, settingsdefs.KeyBrandingDefault, `{"primary":"#1f9fb7"}`))
}

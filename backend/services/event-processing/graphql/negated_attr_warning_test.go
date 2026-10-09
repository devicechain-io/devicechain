// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The negated-attribute-guard advisory must reach the author on every door that compiles a rule,
// without changing whether the rule is accepted.

const (
	fallbackRule = `{"name":"hot","type":"threshold","severity":"major","when":{"cel":"!(\"lim\" in attr) && \"t\" in m && m[\"t\"] > 80.0"},"actions":[{"type":"raiseAlarm","raiseAlarm":{"alarmKey":"hot"}}]}`
	guardedRule  = `{"name":"hot","type":"threshold","severity":"major","when":{"cel":"(\"lim\" in attr) && \"t\" in m && m[\"t\"] > attr[\"lim\"]"},"actions":[{"type":"raiseAlarm","raiseAlarm":{"alarmKey":"hot"}}]}`
)

func TestValidateDetectionRules_CarriesTheNegatedGuardWarning(t *testing.T) {
	r := &SchemaResolver{}
	res, err := r.ValidateDetectionRules(authedContext(auth.DeviceRead), struct{ Rules []detectionRuleInput }{
		Rules: []detectionRuleInput{
			{Token: "guarded", Definition: guardedRule},
			{Token: "fallback", Definition: fallbackRule},
		},
	})
	require.NoError(t, err)
	assert.True(t, res.Valid(), "a warning must never make the batch invalid")
	require.Len(t, res.Warnings(), 1)
	w := res.Warnings()[0]
	assert.Equal(t, int32(1), w.Index())
	assert.Equal(t, "fallback", w.Token())
	assert.Equal(t, "negatedAttributeGuard", w.Code())
	assert.Equal(t, []string{"lim"}, w.Params())
	assert.Contains(t, w.Message(), `"lim"`)
}

func TestCompileCanvas_CarriesTheNegatedGuardWarning(t *testing.T) {
	r := &SchemaResolver{}
	graph := `{"schemaVersion":1,
		"nodes":[
			{"id":"s","type":"source","config":{"scope":{"kind":"profile","profileToken":"thermostat"}}},
			{"id":"c","type":"threshold","config":{"name":"hot","severity":"critical","when":{"cel":"!(\"lim\" in attr) && \"t\" in m && m[\"t\"] > 80.0"}}},
			{"id":"a","type":"action","config":{"action":"raiseAlarm","alarmKey":"hot"}}
		],
		"edges":[{"from":"s:out","to":"c:in"},{"from":"c:signal","to":"a:in"}]}`
	res, err := r.CompileCanvas(authedContext(auth.DeviceRead), struct {
		Graph        string
		ProfileToken string
	}{Graph: graph, ProfileToken: "thermostat"})
	require.NoError(t, err)
	require.True(t, res.Ok(), "%+v", res.Diagnostics())
	require.Len(t, res.Diagnostics(), 1)
	d := res.Diagnostics()[0]
	assert.Equal(t, "warning", d.Severity())
	require.NotNil(t, d.NodeId())
	assert.Equal(t, "c", *d.NodeId(), "anchored on the condition node")
	require.NotNil(t, d.Code())
	assert.Equal(t, "negatedAttributeGuard", *d.Code())
	assert.Equal(t, []string{"lim"}, d.Params())
}

func TestDraftRule_CarriesTheNegatedGuardWarning(t *testing.T) {
	res, err := wiredResolver(fallbackRule).DraftDetectionRuleFromText(draftCtx(), draftInput(draftRuleFromTextInput{Text: "x", ProfileToken: "p"}))
	require.NoError(t, err)
	require.True(t, res.Ok())
	require.Len(t, res.Warnings(), 1)
	w := res.Warnings()[0]
	require.NotNil(t, w.Code())
	assert.Equal(t, "negatedAttributeGuard", *w.Code())
	assert.Equal(t, []string{"lim"}, w.Params())

	clean, err := wiredResolver(guardedRule).DraftDetectionRuleFromText(draftCtx(), draftInput(draftRuleFromTextInput{Text: "x", ProfileToken: "p"}))
	require.NoError(t, err)
	require.True(t, clean.Ok())
	assert.Empty(t, clean.Warnings())
}

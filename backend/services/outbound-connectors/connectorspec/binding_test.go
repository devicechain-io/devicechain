// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package connectorspec

import (
	"reflect"
	"strings"
	"testing"
)

var configs = []struct {
	config any
	fields map[string]bool
}{
	{mqttConfig{}, mqttFields}, {kafkaConfig{}, kafkaFields}, {awsCommon{}, awsFields}, {snsConfig{}, snsFields}, {sqsConfig{}, sqsFields}, {lorawanConfig{}, lorawanFields},
}

func TestCredentialConfigFieldsAreClassified(t *testing.T) {
	for _, tc := range configs {
		var walk func(reflect.Type, string)
		walk = func(typ reflect.Type, prefix string) {
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				if f.Anonymous {
					walk(f.Type, prefix)
					continue
				}
				name := prefix + strings.Split(f.Tag.Get("json"), ",")[0]
				if _, ok := tc.fields[name]; !ok {
					t.Errorf("unclassified field %s.%s (%s)", typ.Name(), f.Name, name)
				}
				if f.Type.Kind() == reflect.Pointer && f.Type.Elem().Kind() == reflect.Struct {
					walk(f.Type.Elem(), name+".")
				}
			}
		}
		walk(reflect.TypeOf(tc.config), "")
	}
}

func TestConnectorBindingNeutralFieldsAndDefaults(t *testing.T) {
	for _, tc := range []struct{ kind, before, after string }{
		{"mqtt", `{"urls":["tcp://one:1883","tcp://two:1883"],"topic":"a"}`, `{"urls":["tcp://two:1883","tcp://one:1883","tcp://one:1883"],"topic":"b","qos":2,"username":"user","clientId":"client"}`},
		{"kafka", `{"addresses":["one:9092","two:9092"],"topic":"a","sasl":{"mechanism":"PLAIN","username":"one"}}`, `{"addresses":["two:9092","one:9092"],"topic":"b","clientId":"client","tls":false,"sasl":{"mechanism":"PLAIN","username":"two"}}`},
		{"aws_sns", `{"region":"us-east-1","accessKeyId":"one","topicArn":"arn:one"}`, `{"region":"us-east-1","accessKeyId":"two","topicArn":"arn:two"}`},
		{"aws_sqs", `{"region":"us-east-1","accessKeyId":"one","url":"https://queue.example/one"}`, `{"region":"us-east-1","accessKeyId":"two","url":"https://queue.example/two"}`},
		{"lorawan_downlink", `{"networkServer":"tts_v3","baseUrl":"https://network.example/","integration":"one","applicationId":"app-one"}`, `{"networkServer":"tts_v3","baseUrl":"https://network.example","integration":"two","applicationId":"app-two"}`},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			if err := ValidateConfig(tc.kind, []byte(tc.before)); err != nil {
				t.Fatal(err)
			}
			if err := ValidateConfig(tc.kind, []byte(tc.after)); err != nil {
				t.Fatal(err)
			}
			if changed := Binding(tc.kind, []byte(tc.before)).Diff(Binding(tc.kind, []byte(tc.after))); len(changed) != 0 {
				t.Fatalf("neutral edit changed binding: %v", changed)
			}
		})
	}
}
func TestUnsupportedAndUnparseableBindingsAreOpaque(t *testing.T) {
	for _, kind := range []string{"unknown", "mqtt"} {
		before := Binding(kind, []byte(`{"legacy":"one"}`))
		after := Binding(kind, []byte(`{"legacy":"two"}`))
		if changed := before.Diff(after); len(changed) != 1 || changed[0] != "config" {
			t.Fatalf("opaque change=%v", changed)
		}
		if len(before.Diff(before)) != 0 {
			t.Fatal("opaque no-op changed")
		}
	}
}

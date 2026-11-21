// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func generatedSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := GenerateInstanceJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	const location = "https://devicechain.invalid/instance-config.schema.json"
	if err := c.AddResource(location, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(location)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const minimalInstance = `{"infrastructure":{"nats":{"hostname":"nats","port":4222},"userManagement":{"hostname":"users","port":8080}}}`

func assertSchemaStartup(t *testing.T, schema *jsonschema.Schema, raw string, valid bool) {
	t.Helper()
	v, err := jsonschema.UnmarshalJSON(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(v); (err == nil) != valid {
		t.Errorf("schema valid=%v, want %v: %v; %s", err == nil, valid, err, raw)
	}
	var cfg InstanceConfiguration
	err = json.Unmarshal([]byte(raw), &cfg)
	if err == nil {
		cfg.ApplyDefaults()
		err = cfg.Validate()
	}
	if (err == nil) != valid {
		t.Errorf("startup valid=%v, want %v: %v; %s", err == nil, valid, err, raw)
	}
}

func TestInstanceSchemaMandatoryEndpoints(t *testing.T) {
	s := generatedSchema(t)
	for _, raw := range []string{`{}`, `null`, `{"infrastructure":null}`, `{"infrastructure":{}}`} {
		assertSchemaStartup(t, s, raw, false)
	}
	for _, endpoint := range []string{"nats", "userManagement"} {
		for _, field := range []string{"hostname", "port"} {
			for _, value := range []any{nil, "missing", "zero"} {
				var doc map[string]any
				if err := json.Unmarshal([]byte(minimalInstance), &doc); err != nil {
					t.Fatal(err)
				}
				obj := doc["infrastructure"].(map[string]any)[endpoint].(map[string]any)
				switch value {
				case "missing":
					delete(obj, field)
				case "zero":
					if field == "port" {
						obj[field] = 0
					} else {
						obj[field] = ""
					}
				default:
					obj[field] = nil
				}
				raw, _ := json.Marshal(doc)
				assertSchemaStartup(t, s, string(raw), false)
			}
		}
		for _, missing := range []bool{false, true} {
			var doc map[string]any
			if err := json.Unmarshal([]byte(minimalInstance), &doc); err != nil {
				t.Fatal(err)
			}
			infra := doc["infrastructure"].(map[string]any)
			if missing {
				delete(infra, endpoint)
			} else {
				infra[endpoint] = nil
			}
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			assertSchemaStartup(t, s, string(raw), false)
		}
	}
	assertSchemaStartup(t, s, minimalInstance, true)
	assertSchemaStartup(t, s, strings.NewReplacer("infrastructure", "Infraſtructure", "hostname", "HOSTNAME", "port", "PORT", "userManagement", "USERMANAGEMENT").Replace(minimalInstance), true)
}

func TestInstanceSchemaStaticValidation(t *testing.T) {
	s := generatedSchema(t)
	for _, tc := range []struct {
		block, field string
		good, bad    []string
	}{
		{"logging", "level", []string{`null`, `""`, `"trace"`, `"debug"`, `"info"`, `"warn"`, `"error"`}, []string{`"fatal"`, `"INFO"`}},
		{"tracing", "protocol", []string{`null`, `""`, `"grpc"`, `"http"`}, []string{`"udp"`, `"GRPC"`}},
		{"tracing", "samplingRatio", []string{`null`, `0`, `1`}, []string{`-0.1`, `1.1`}},
		{"shutdown", "drainSeconds", []string{`null`, `0`, `15`}, []string{`-1`}},
		{"shutdown", "terminationGracePeriodSeconds", []string{`null`, `0`, `30`}, []string{`-1`}},
		{"messaging", "wireFormat", []string{`null`, `""`, `"legacy"`, `"compact"`}, []string{`"other"`}},
		{"tokenAudience", "enforcement", []string{`null`, `""`, `"enforce"`}, []string{`"observe"`}},
		{"tokenAudience", "unscopedClientCredentials", []string{`null`, `""`, `"observe"`}, []string{`"enforce"`}},
		{"messaging.envelopes", "inboundLingerMillis", []string{`null`, `0`, `10`}, []string{`-1`, `11`}},
		{"nats.fetch", "batch", []string{`null`, `0`, `1`}, []string{`-1`, `2147483647`}},
		{"nats.fetch", "aheadHoldBudgetMillis", []string{`null`, `0`, `1`}, []string{`-1`}},
		{"nats", "streamReplicas", []string{`null`, `0`, `1`, `3`, `5`}, []string{`2`, `4`, `6`}},
		{"nats", "streamDesignRate", []string{`null`, `0`, `1000000`}, []string{`-1`, `1000001`}},
		{"nats", "streamRunwayMinutes", []string{`null`, `0`, `10`, `10080`}, []string{`-1`, `1`, `10081`}},
		{"nats", "port", []string{`1`, `65535`}, []string{`65536`}},
		{"userManagement", "internalPort", []string{`null`, `0`, `65535`}, []string{`65536`}},
	} {
		for _, group := range []struct {
			values []string
			valid  bool
		}{{tc.good, true}, {tc.bad, false}} {
			for _, value := range group.values {
				var doc map[string]any
				if err := json.Unmarshal([]byte(minimalInstance), &doc); err != nil {
					t.Fatal(err)
				}
				obj := doc["infrastructure"].(map[string]any)
				for _, name := range strings.Split(tc.block, ".") {
					if obj[name] == nil {
						obj[name] = map[string]any{}
					}
					obj = obj[name].(map[string]any)
				}
				obj[tc.field] = json.RawMessage(value)
				raw, err := json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				t.Run(tc.block+"/"+tc.field+"/"+value, func(t *testing.T) { assertSchemaStartup(t, s, string(raw), group.valid) })
			}
		}
	}
}

func TestInstanceSchemaIntegerKeyOverflow(t *testing.T) {
	s := generatedSchema(t)
	for _, key := range []string{"9223372036854775808", "-9223372036854775809", "0009223372036854775808"} {
		raw := strings.Replace(minimalInstance, `"port":4222`, fmt.Sprintf(`"port":4222,"shardGroups":{"count":2,"placementTags":{%q:["zone-a"]}}`, key), 1)
		assertSchemaStartup(t, s, raw, false)
	}
	for _, key := range []string{"0", "1", "+1", "0001", "-0"} {
		raw := strings.Replace(minimalInstance, `"port":4222`, fmt.Sprintf(`"port":4222,"shardGroups":{"count":2,"placementTags":{%q:["zone-a"]}}`, key), 1)
		assertSchemaStartup(t, s, raw, true)
	}
}

func TestInstanceSchemaIntegerRepresentationLimitation(t *testing.T) {
	s := generatedSchema(t)
	for _, number := range []string{"4222.0", "4.222e3"} {
		raw := strings.Replace(minimalInstance, "4222", number, 1)
		v, err := jsonschema.UnmarshalJSON(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Validate(v); err != nil {
			t.Fatal(err)
		}
		var cfg InstanceConfiguration
		if err := json.Unmarshal([]byte(raw), &cfg); err == nil {
			t.Fatal("Go decoder unexpectedly accepted non-integer token")
		}
	}
	raw, err := GenerateInstanceJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	guide, err := os.ReadFile("SCHEMA.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range []string{string(raw), string(guide)} {
		for _, term := range []string{"4222.0", "4.222e3", "encoding/json"} {
			if !strings.Contains(doc, term) {
				t.Errorf("integer representation limitation must document %s", term)
			}
		}
	}
}

func TestInstanceSchemaIntegerMapKeyRanges(t *testing.T) {
	for _, keyType := range []reflect.Type{
		reflect.TypeFor[int](), reflect.TypeFor[int8](), reflect.TypeFor[int16](), reflect.TypeFor[int32](), reflect.TypeFor[int64](),
		reflect.TypeFor[uint](), reflect.TypeFor[uint8](), reflect.TypeFor[uint16](), reflect.TypeFor[uint32](), reflect.TypeFor[uint64](),
	} {
		t.Run(keyType.String(), func(t *testing.T) {
			mapType := reflect.MapOf(keyType, reflect.TypeFor[[]string]())
			b := schemaBuilder{defs: map[string]any{}}
			node, err := b.node(mapType)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(node)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			c := jsonschema.NewCompiler()
			if err := c.AddResource("map.json", doc); err != nil {
				t.Fatal(err)
			}
			s, err := c.Compile("map.json")
			if err != nil {
				t.Fatal(err)
			}
			bits := keyType.Bits()
			if keyType.Kind() >= reflect.Int && keyType.Kind() <= reflect.Int64 {
				bits--
			}
			boundary := new(big.Int).Lsh(big.NewInt(1), uint(bits))
			max := new(big.Int).Sub(new(big.Int).Set(boundary), big.NewInt(1))
			keys := []string{"0", "-0", "+0", "1", "01", "+01", "-01", "", "1.0", "1e0", " 1", "1\n", "++1", max.String(), boundary.String(), "000" + max.String(), "000" + boundary.String(), "-" + max.String(), "-" + boundary.String(), "-" + new(big.Int).Add(boundary, big.NewInt(1)).String()}
			for _, key := range keys {
				raw := fmt.Sprintf(`{%q:["tag"]}`, key)
				decodeErr := json.Unmarshal([]byte(raw), reflect.New(mapType).Interface())
				value, err := jsonschema.UnmarshalJSON(strings.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				schemaErr := s.Validate(value)
				if (schemaErr == nil) != (decodeErr == nil) {
					t.Errorf("%s key %q: schema=%v; decoder=%v", keyType, key, schemaErr, decodeErr)
				}
			}
		})
	}
}

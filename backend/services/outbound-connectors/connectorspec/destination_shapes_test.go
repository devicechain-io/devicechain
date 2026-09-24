// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package connectorspec

import (
	"testing"
)

type shapeRow struct {
	typ, config string
}

func mqttRow(url string) shapeRow {
	return shapeRow{"mqtt", `{"urls":["` + url + `"],"topic":"t"}`}
}

func kafkaRow(addr string) shapeRow {
	return shapeRow{"kafka", `{"addresses":["` + addr + `"],"topic":"t"}`}
}

func snsEndpointRow(endpoint string) shapeRow {
	return shapeRow{"aws_sns", `{"region":"us-east-1","accessKeyId":"AKIA","topicArn":"arn:aws:sns:us-east-1:1:t","endpoint":"` + endpoint + `"}`}
}

func snsRegionRow(region string) shapeRow {
	return shapeRow{"aws_sns", `{"region":"` + region + `","accessKeyId":"AKIA","topicArn":"arn:aws:sns:us-east-1:1:t"}`}
}

// The shapes a destination may take are refused where they are authored AND again where
// they are dispatched, by the same rule. Each row is run through both entry points,
// because a rule that lived in only one of them would pass half of this table.
//
// The refused rows are the ones that reach something other than a TCP destination the
// egress guard can judge (a unix socket, a scheme the client would interpret its own
// way), smuggle a second destination past a per-entry check (a comma), or carry parts
// the client would silently drop or reinterpret (userinfo, a path on a raw TCP URL).
func TestRefusedDestinationShapes(t *testing.T) {
	refused := []shapeRow{
		mqttRow("unix:///x"),
		mqttRow("tcps://h:1"),
		mqttRow("mqtt+ssl://h:1"),
		mqttRow("http://h:1"),
		mqttRow("tcp://h:1883,unix:///x"),
		mqttRow("tcp://u:p@h:1883"),
		mqttRow("tcp://h"),
		mqttRow("tcp://:1883"),
		mqttRow("tcp://h:1883/p"),
		mqttRow("ws://h:80?x=1"),
		mqttRow("TCP://h:1883"),
		mqttRow("tcp://h:0"),
		mqttRow("tcp://h:65536"),
		kafkaRow("tcp://h:9092"),
		kafkaRow("h"),
		kafkaRow("h:0"),
		kafkaRow("h:70000"),
		kafkaRow("/var/run/k.sock"),
		kafkaRow("a:1,b:2"),
		snsEndpointRow("unix:///x"),
		snsEndpointRow("ftp://h"),
		snsEndpointRow("h:443"),
		snsEndpointRow("http://u:p@h"),
		snsEndpointRow("https://h/path"),
		snsEndpointRow("https://h/?q=1"),
		snsRegionRow("us-east-1.evil.example"),
		snsRegionRow("a/b"),
		{"aws_sns", `{"region":"us-east-1","accessKeyId":"AKIA","topicArn":"not-an-arn"}`},
		{"aws_sqs", `{"region":"us-east-1","accessKeyId":"AKIA","url":"sqs.example/q"}`},
	}
	accepted := []shapeRow{
		mqttRow("tcp://h:1883"),
		mqttRow("mqtt://h:1883"),
		mqttRow("ssl://h:8883"),
		mqttRow("tls://h:8883"),
		mqttRow("mqtts://h:8883"),
		mqttRow("ws://h:80/mqtt"),
		mqttRow("wss://h:443/mqtt"),
		mqttRow("wss://h:443"),
		kafkaRow("h:9092"),
		kafkaRow("[::1]:9092"),
		snsEndpointRow("https://sns.x"),
		snsEndpointRow("http://localstack:4566/"),
		{"aws_sqs", `{"region":"us-east-1","accessKeyId":"AKIA","url":"https://sqs.us-east-1.amazonaws.com/1/q"}`},
	}

	for _, r := range refused {
		if err := ValidateConfig(r.typ, []byte(r.config)); err == nil {
			t.Errorf("ValidateConfig accepted %s %s", r.typ, r.config)
		}
		if _, err := Build(r.typ, []byte(r.config), "s"); err == nil {
			t.Errorf("Build accepted %s %s", r.typ, r.config)
		}
	}
	for _, r := range accepted {
		if err := ValidateConfig(r.typ, []byte(r.config)); err != nil {
			t.Errorf("ValidateConfig refused %s %s: %v", r.typ, r.config, err)
		}
		if _, err := Build(r.typ, []byte(r.config), "s"); err != nil {
			t.Errorf("Build refused %s %s: %v", r.typ, r.config, err)
		}
	}
}

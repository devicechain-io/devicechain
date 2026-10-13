// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lwm2mpath

import "testing"

func TestIsFactoryResetPath(t *testing.T) {
	for _, p := range []string{"/3/0/5", "3/0/5", "/3/0/5/", " /3/0/5 ", "/3/00/5", "/03/0/05", "/3/7/5", "/3/65535/5",
		"/3/0/5/0", "//3/0/5", "/3//0/5"} {
		if !IsFactoryResetPath(p) {
			t.Errorf("%q must be refused", p)
		}
	}
	for _, p := range []string{"/3/0/4", "/3/0", "/3", "/3/0/50", "/4/0/5", "/33/0/5", "/3/0/6", "", "/", "/3/0/x", "/3/0/-5", "/3/0/+5"} {
		if IsFactoryResetPath(p) {
			t.Errorf("%q must not be refused", p)
		}
	}
}

func TestIsRefusedRawExecute(t *testing.T) {
	if !IsRefusedRawExecute("lwm2m.execute", []byte(`{"path":"/3/0/5"}`)) {
		t.Error("raw factory reset must be refused")
	}
	if !IsRefusedRawExecute("lwm2m.execute", []byte(`{"Path":"3/0/5/","args":"x"}`)) {
		t.Error("variant spelling must be refused")
	}
	for _, c := range []struct {
		key     string
		payload string
	}{
		{"lwm2m.execute", `{"path":"/3/0/4"}`},
		{"lwm2m.execute", `{"path":"/5/0/2"}`},
		{"lwm2m.execute", `not json`},
		{"lwm2m.execute", ``},
		{"lwm2m.read", `{"path":"/3/0/5"}`},
		{"lwm2m.reboot", `{"path":"/3/0/5"}`},
	} {
		if IsRefusedRawExecute(c.key, []byte(c.payload)) {
			t.Errorf("%s %s must not be refused", c.key, c.payload)
		}
	}
}

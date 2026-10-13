// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"
)

// The standard-object (OMA Object 3, Device) operations the adapter offers by NAME. They are
// ordinary command keys, exactly like the three path-addressed ones: a profile declares the key as a
// command definition, so the enqueue gate, the command:write authorization, the audit journal and the
// whole claim/park/drain machinery apply to them unchanged. What the name adds is that the operator
// no longer hand-types the object path.
const (
	// CommandReboot executes Object 3's Reboot resource (/3/0/4). It takes no parameters.
	CommandReboot = "lwm2m.reboot"
	// CommandReadDeviceInfo reads the supported Device-object information resources and answers with
	// a JSON document of the values read plus a list of the resources it could not read.
	CommandReadDeviceInfo = "lwm2m.readDeviceInfo"
)

// Factory Reset (/3/0/5) is deliberately NOT a named operation. It is destructive and irreversible,
// and a command key on a profile is a one-click send; it needs an explicit confirmation flow that
// has not been decided yet. A device that supports it can still be sent lwm2m.execute with that
// path by an operator who types it, which is the same deliberate act it always was.

const (
	labelReboot     = "reboot"
	labelDeviceInfo = "device_info"
)

// rebootPath is Object 3, instance 0, resource 4 (Reboot).
const rebootPath = "/3/0/4"

// maxInfoValueBytes bounds one harvested string. The value is device-controlled text that ends up in
// an attribute shown to operators, so it is capped and stripped of anything that is not printable.
const maxInfoValueBytes = 256

// infoKind is how a Device-object resource is turned into an attribute value.
type infoKind int

const (
	infoString infoKind = iota
	// infoPercent is a battery level: an integer 0-100.
	infoPercent
)

// infoResource describes one harvested Object 3 resource. Key is the attribute key it is stored
// under (CLIENT scope) and the field name in a readDeviceInfo answer; they are the same name on
// purpose, so an operator comparing the two sees one vocabulary.
type infoResource struct {
	Key  string
	Path string
	Kind infoKind
}

// deviceInfoResources is the supported set, in the order it is read. Manufacturer, Model Number,
// Serial Number and Firmware Version are Object 3's mandatory-or-common identity resources; Battery
// Level is optional and absent on mains-powered devices, which is an ordinary outcome and not a
// fault.
var deviceInfoResources = []infoResource{
	{Key: "manufacturer", Path: "/3/0/0", Kind: infoString},
	{Key: "modelNumber", Path: "/3/0/1", Kind: infoString},
	{Key: "serialNumber", Path: "/3/0/2", Kind: infoString},
	{Key: "firmwareVersion", Path: "/3/0/3", Kind: infoString},
	{Key: "batteryLevel", Path: "/3/0/9", Kind: infoPercent},
}

// InfoMissing names one resource that was not harvested and why.
type InfoMissing struct {
	Resource string `json:"resource"`
	Path     string `json:"path"`
	Reason   string `json:"reason"`
}

// DeviceInfo is the outcome of reading the Device object: the values that were read, keyed by
// attribute key, and a record of every resource that was not. Values and Missing together always
// cover the whole supported set, so "not reported" is never confused with "not asked".
type DeviceInfo struct {
	Values  map[string]string
	Missing []InfoMissing
	// Aborted is set when a transport failure (the device stopped answering) ended the read early.
	// Resources not yet asked are listed in Missing with that reason. A device that merely lacks a
	// resource (4.04) does not abort anything.
	Aborted bool
}

// valueType returns the attribute value type for a harvested key.
func infoValueType(key string) string {
	for _, r := range deviceInfoResources {
		if r.Key == key && r.Kind == infoPercent {
			return "LONG"
		}
	}
	return "STRING"
}

// ReadDeviceInfo reads each supported Object 3 resource with its own single-resource GET. One
// attempt per resource, each bounded by perRead; a resource the device does not have (4.04), will
// not give (4.01/4.03/4.05) or returns in a format this slice does not decode is RECORDED in Missing
// and skipped, never retried and never an error. The first transport failure (timeout, dead
// socket) stops the read: a device that is not answering is not asked four more times.
//
// No Accept option is sent, for the reason lwm2m.read sends none: a conformant 1.0 client answers
// 4.06 to a format it does not implement, and the single-resource text/plain answer is what every
// client gives.
func ReadDeviceInfo(ctx context.Context, conn mux.Conn, perRead time.Duration) DeviceInfo {
	info := DeviceInfo{Values: map[string]string{}}
	for i, res := range deviceInfoResources {
		if ctx.Err() != nil {
			info.abortFrom(i, "device did not respond (timeout)")
			return info
		}
		val, reason, transport := readInfoResource(ctx, conn, res, perRead)
		switch {
		case transport:
			info.Missing = append(info.Missing, InfoMissing{Resource: res.Key, Path: res.Path, Reason: reason})
			info.abortFrom(i+1, reason)
			return info
		case reason != "":
			info.Missing = append(info.Missing, InfoMissing{Resource: res.Key, Path: res.Path, Reason: reason})
		default:
			info.Values[res.Key] = val
		}
	}
	return info
}

// abortFrom records every resource from index i onward as not attempted.
func (d *DeviceInfo) abortFrom(i int, reason string) {
	d.Aborted = true
	for _, res := range deviceInfoResources[i:] {
		d.Missing = append(d.Missing, InfoMissing{Resource: res.Key, Path: res.Path, Reason: "not read: " + reason})
	}
}

// readInfoResource performs one GET. It returns the sanitized value, or a reason it is missing;
// transport reports a failure of the exchange itself (as opposed to the device answering no).
func readInfoResource(ctx context.Context, conn mux.Conn, res infoResource, perRead time.Duration) (val, reason string, transport bool) {
	rctx, cancel := context.WithTimeout(ctx, perRead)
	defer cancel()
	resp, err := conn.Get(rctx, res.Path)
	if err != nil {
		if rctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			return "", "device did not respond (timeout)", true
		}
		return "", "transport error contacting the device", true
	}
	if !isSuccess(resp.Code()) {
		return "", describeInfoCode(resp.Code()), false
	}
	if cf, cfErr := resp.ContentFormat(); cfErr == nil && cf != message.TextPlain {
		return "", fmt.Sprintf("unsupported content format %d (only text/plain is decoded)", cf), false
	}
	body, err := resp.ReadBody()
	if err != nil {
		return "", "response body was unreadable", false
	}
	text, ok := sanitizeInfoText(body)
	if !ok {
		return "", "empty or non-text value", false
	}
	if res.Kind == infoPercent {
		n, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 100 || n != math.Trunc(n) {
			return "", "value is not an integer percentage (0-100)", false
		}
		return strconv.FormatInt(int64(n), 10), "", false
	}
	return text, "", false
}

// describeInfoCode phrases a non-2.xx answer for the Missing record.
func describeInfoCode(code codes.Code) string {
	switch code {
	case codes.NotFound:
		return "not reported by the device (4.04 Not Found)"
	case codes.Unauthorized, codes.Forbidden, codes.MethodNotAllowed:
		return "not readable by the server (" + code.String() + ")"
	default:
		return "device returned " + code.String()
	}
}

// sanitizeInfoText makes device-supplied bytes safe to store and show: valid UTF-8 only, control
// and format characters removed, surrounding space trimmed, capped at maxInfoValueBytes. ok is
// false when nothing printable is left.
func sanitizeInfoText(b []byte) (string, bool) {
	if !utf8.Valid(b) {
		return "", false
	}
	var sb strings.Builder
	for _, r := range string(b) {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if sb.Len()+utf8.RuneLen(r) > maxInfoValueBytes {
			break
		}
		sb.WriteRune(r)
	}
	s := strings.TrimSpace(sb.String())
	return s, s != ""
}

// infoAnswer is the JSON document lwm2m.readDeviceInfo answers with. Numbers are numbers (battery
// level), strings are strings.
func (d DeviceInfo) answer() map[string]any {
	out := map[string]any{}
	for _, res := range deviceInfoResources {
		v, ok := d.Values[res.Key]
		if !ok {
			continue
		}
		if res.Kind == infoPercent {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				out[res.Key] = n
				continue
			}
		}
		out[res.Key] = v
	}
	missing := d.Missing
	if missing == nil {
		missing = []InfoMissing{}
	}
	out["missing"] = missing
	return out
}

// readDeviceInfo is the lwm2m.readDeviceInfo executor. It succeeds when at least one resource was
// read; a device that answered for none of them fails the command with the reasons, so a wrong
// device (no Device object) or a dead radio is not reported as a success with an empty document.
func (o *Ops) readDeviceInfo(ctx context.Context, conn mux.Conn) OpResult {
	info := ReadDeviceInfo(ctx, conn, infoReadTimeout(ctx))
	if len(info.Values) == 0 {
		return OpResult{Op: labelDeviceInfo, Success: false,
			Err: strptr("no Device-object resource could be read: " + summarizeMissing(info.Missing))}
	}
	b, err := json.Marshal(info.answer())
	if err != nil {
		return OpResult{Op: labelDeviceInfo, Success: false, Err: strptr("device information could not be encoded")}
	}
	return OpResult{Op: labelDeviceInfo, Success: true, Payload: strptr(string(b))}
}

// infoReadTimeout gives each single read the time left on the command's own deadline, so the
// command's overall bound (the dispatcher's op timeout) is the only clock.
func infoReadTimeout(ctx context.Context) time.Duration {
	if dl, ok := ctx.Deadline(); ok {
		if d := time.Until(dl); d > 0 {
			return d
		}
	}
	return DefaultOpTimeout
}

// summarizeMissing renders the first few reasons for an error string.
func summarizeMissing(m []InfoMissing) string {
	if len(m) == 0 {
		return "none attempted"
	}
	parts := make([]string, 0, 3)
	for i, x := range m {
		if i == 3 {
			parts = append(parts, "...")
			break
		}
		parts = append(parts, x.Resource+": "+x.Reason)
	}
	return strings.Join(parts, "; ")
}

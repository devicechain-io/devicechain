// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/userclient"
)

// The bulk-change drill's client half: hack/upgrade-rig.sh's `bulkchange` phase drives a bulk change
// through an upgraded device-management pod while a BASELINE pod writes and deletes the same devices,
// and every action that needs the API is one of these subcommands. The rig keeps the orchestration and
// the database reads; this keeps the GraphQL, the paging and the verdicts, so the verdicts are pure
// functions the unit tests can hand every state they must tell apart (bulkchange_test.go).
//
//   apiprobe bulkchange seed     --receipt R --url DM --out B.json [--count N] [--prefix P]
//   apiprobe bulkchange start    --receipt R --bulk B.json --url DM --token T --value V [--first N --last M] [--rate R]
//   apiprobe bulkchange status   --receipt R --url DM --token T
//   apiprobe bulkchange abort    --receipt R --url DM --token T
//   apiprobe bulkchange lines    --receipt R --url DM --token T --out lines.json
//   apiprobe bulkchange oldwrite --receipt R --bulk B.json --url DM --mode-devices F --warm-devices F
//                                --expect accepted|refused|any --stop-file F --out result.json [--toggle]
//   apiprobe bulkchange delete   --receipt R --url DM --device T
//   apiprobe bulkchange judge    --lines lines.json --count N [--deleted 3,1998] [--report-log]
//   apiprobe bulkchange redacted --lines lines.json --ordinals 3,1998 [--expect redacted|unredacted]
//   apiprobe bulkchange priors   --rows rows.tsv --key K [--expect correct|wrong]
//   apiprobe bulkchange report   (bulkchange_report.go)
//   apiprobe bulkchange lockkey  --name N   the advisory-lock key a singleton pass of that name takes
//   apiprobe bulkchange wait     --receipt R --url DM --token T --until half-posted|dispatched|closed [--timeout D]
//   apiprobe bulkchange redaction --receipt R --url DM --token T --ordinals 3,1998 --within D
//                                [--expect redacted|unredacted] --out lines.json
//   apiprobe bulkchange attrload (bulkchange_report.go: the P-1 interactive load)
//
// wait and redaction poll inside ONE process (bulkchange_wait.go), so one sign-in serves the whole wait
// rather than one sign-in per poll.
//
// 🔴 EVERY CALL IS ADDRESSED TO ONE POD (--url), never the ingress, for the reason attrlock gives: the
// baseline copy keeps the live pods' labels, so the Service load-balances onto it, and a bulk-change call
// that lands on a baseline pod is answered "unknown field" for a reason that is not the release's.

// The drill's own device-visible configuration: two declared STRING keys on a profile of its own, so the
// seed's probe profile, device and receipt are left exactly as verify will read them.
//
//	mode  the key the bulk change sets. A quarter of the devices start WITHOUT it, so the change and the
//	      baseline writes include first writes of an absent key.
//	warm  written on every device at seed time, so every device has a configuration revision (and so a
//	      report it can make) before the change starts, and written by the baseline pod during it.
const (
	bcModeSuffix  = "-mode"
	bcWarmSuffix  = "-warm"
	bcValueType   = "STRING"
	bcMaxPerBatch = 1000 // createDevices refuses more
	bcLinesPage   = 500
)

// bulkReceipt is what `bulkchange seed` wrote, read by every later step. Devices[i] is device n = i+1.
type bulkReceipt struct {
	Prefix     string   `json:"prefix"`
	Profile    string   `json:"profile"`
	DeviceType string   `json:"deviceType"`
	ModeKey    string   `json:"modeKey"`
	WarmKey    string   `json:"warmKey"`
	Devices    []string `json:"devices"`
}

// bcModeSeeded says whether device n (1-based) was given the mode key at seed time: every device but each
// fourth, so a quarter start without it.
func bcModeSeeded(n int) bool { return n%4 != 0 }

// bcSeedValue is the mode value device n holds after the seed.
func bcSeedValue(n int) string { return fmt.Sprintf("seed-%d", n) }

const (
	bcCreateProfileDoc = `mutation($req:DeviceProfileCreateRequest!){createDeviceProfile(request:$req){token}}`
	bcDeclareDoc       = `mutation($token:String!,$keys:[ConfigurationKeyInput!]!){setDeviceProfileConfigurationDeclaration(token:$token,keys:$keys){token}}`
	bcPublishDoc       = `mutation($token:String!,$label:String){publishDeviceProfile(token:$token,label:$label){version}}`
	bcCreateTypeDoc    = `mutation($req:DeviceTypeCreateRequest!){createDeviceType(request:$req){token}}`
	bcCreateDevicesDoc = `mutation($req:DeviceBulkCreateRequest!){createDevices(request:$req){token}}`
	bcUnsetDoc         = `mutation($entityType:String!,$entity:String!,$scope:String!,$attrKey:String!){deleteEntityAttribute(entityType:$entityType,entity:$entity,scope:$scope,attrKey:$attrKey)}`
	bcDeleteDeviceDoc  = `mutation($token:String!){deleteDevice(token:$token)}`

	bcChangeFields = `token dispatch evidence pauseReason holdCode lastErrorCode contractDigest targetCount ` +
		`evidenceCapability{reportLog brokerFailClosed} ` +
		`progress{planned posted refused skipped cancelled confirmed failed superseded ambiguous resolved awaitingEvidence}`
	bcPlanDoc   = `query($request:BulkChangeCreateRequest!){plan:planBulkChange(request:$request){admittedCount refusedCount contractDigest}}`
	bcCreateDoc = `mutation($request:BulkChangeCreateRequest!){change:createBulkChange(request:$request){` + bcChangeFields + `}}`
	bcStartDoc  = `mutation($token:String!,$digest:String!){change:startBulkChange(token:$token,contractDigest:$digest){` + bcChangeFields + `}}`
	bcStatusDoc = `query($token:String!){change:bulkChange(token:$token){` + bcChangeFields + `}}`
	bcAbortDoc  = `mutation($token:String!){change:abortBulkChange(token:$token){` + bcChangeFields + `}}`
	bcLinesDoc  = `query($token:String!,$criteria:BulkChangeLineSearchCriteria!){lines:bulkChangeLines(token:$token,criteria:$criteria){` +
		`results{ordinal targetToken redacted posting settlement code prior postedValue postedRevision evidenceBasis} ` +
		`pagination{totalRecords}}}`
)

// bcChange is the part of a BulkChange the drill reads.
type bcChange struct {
	Token              string `json:"token"`
	Dispatch           string `json:"dispatch"`
	Evidence           string `json:"evidence"`
	PauseReason        *string
	HoldCode           *string
	LastErrorCode      *string
	ContractDigest     string `json:"contractDigest"`
	TargetCount        int    `json:"targetCount"`
	EvidenceCapability struct {
		ReportLog        bool `json:"reportLog"`
		BrokerFailClosed bool `json:"brokerFailClosed"`
	} `json:"evidenceCapability"`
	Progress struct {
		Planned          int `json:"planned"`
		Posted           int `json:"posted"`
		Refused          int `json:"refused"`
		Skipped          int `json:"skipped"`
		Cancelled        int `json:"cancelled"`
		Confirmed        int `json:"confirmed"`
		Failed           int `json:"failed"`
		Superseded       int `json:"superseded"`
		Ambiguous        int `json:"ambiguous"`
		Resolved         int `json:"resolved"`
		AwaitingEvidence int `json:"awaitingEvidence"`
	} `json:"progress"`
}

// bcLine is one BulkChangeLine as the drill reads it.
type bcLine struct {
	Ordinal        int     `json:"ordinal"`
	TargetToken    *string `json:"targetToken"`
	Redacted       bool    `json:"redacted"`
	Posting        string  `json:"posting"`
	Settlement     *string `json:"settlement"`
	Code           *string `json:"code"`
	Prior          *string `json:"prior"`
	PostedValue    *string `json:"postedValue"`
	PostedRevision *int64  `json:"postedRevision"`
	EvidenceBasis  *string `json:"evidenceBasis"`
}

func runBulkChange(ctx context.Context, argv []string) error {
	if len(argv) < 1 {
		return failWith(exitSetup, "bulkchange needs an action: seed | start | status | wait | abort | lines | redaction | oldwrite | delete | judge | redacted | priors | report | attrload | lockkey")
	}
	action, rest := argv[0], argv[1:]
	switch action {
	case "seed":
		return bcSeed(ctx, rest, os.Stdout)
	case "start":
		return bcStart(ctx, rest, os.Stdout)
	case "status":
		return bcStatusCmd(ctx, rest, os.Stdout, bcStatusDoc)
	case "abort":
		return bcStatusCmd(ctx, rest, os.Stdout, bcAbortDoc)
	case "lines":
		return bcLinesCmd(ctx, rest, os.Stdout)
	case "oldwrite":
		return bcOldWrite(ctx, rest, os.Stdout)
	case "delete":
		return bcDelete(ctx, rest, os.Stdout)
	case "judge":
		return bcJudgeCmd(rest, os.Stdout)
	case "redacted":
		return bcRedactedCmd(rest, os.Stdout)
	case "priors":
		return bcPriorsCmd(rest, os.Stdout)
	case "report":
		return bcReport(ctx, rest, os.Stdout)
	case "attrload":
		return bcAttrLoad(ctx, rest, os.Stdout)
	case "lockkey":
		return bcLockKey(rest, os.Stdout)
	case "wait":
		return bcWaitCmd(ctx, rest, os.Stdout)
	case "redaction":
		return bcRedactionCmd(ctx, rest, os.Stdout)
	default:
		return failWith(exitSetup, "unknown bulkchange action %q", action)
	}
}

// bcSession is the receipt's tenant identity signed in against the instance, and the one pod's endpoint.
type bcSession struct {
	session *userclient.TenantSession
	url     string
}

// bindSession adds the two flags every API-calling action shares.
func bindSession(fs *flag.FlagSet, receipt, url *string) {
	fs.StringVar(receipt, "receipt", "", "path to the receipt seed wrote (required)")
	fs.StringVar(url, "url", "", "the GraphQL endpoint of ONE device-management pod (required)")
}

func openSession(c connection, receipt, url string) (*bcSession, error) {
	if strings.TrimSpace(receipt) == "" {
		return nil, failWith(exitSetup, "--receipt is required")
	}
	if strings.TrimSpace(url) == "" {
		return nil, failWith(exitSetup, "--url is required: bulk-change calls are addressed to one pod, never the ingress")
	}
	rec, err := readReceipt(receipt)
	if err != nil {
		return nil, err
	}
	c.tenant = rec.Tenant
	return &bcSession{session: c.session(rec.Identity), url: url}, nil
}

func (s *bcSession) query(ctx context.Context, doc string, vars map[string]any, out any) error {
	return s.session.Query(ctx, s.url, doc, vars, out)
}

func readBulkReceipt(path string) (bulkReceipt, error) {
	var b bulkReceipt
	if strings.TrimSpace(path) == "" {
		return b, failWith(exitSetup, "--bulk is required")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return b, failWith(exitSetup, "read %s: %w", path, err)
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return b, failWith(exitSetup, "%s is not a bulk-change receipt: %w", path, err)
	}
	if b.ModeKey == "" || b.WarmKey == "" || len(b.Devices) == 0 {
		return b, failWith(exitSetup, "%s names no keys or no devices", path)
	}
	return b, nil
}

// ---------------------------------------------------------------------------
// seed
// ---------------------------------------------------------------------------

func bcSeed(ctx context.Context, argv []string, stdout io.Writer) error {
	fs := flagSetFor("bulkchange seed")
	var c connection
	c.bind(fs)
	var receipt, url, out, prefix string
	var count, workers int
	bindSession(fs, &receipt, &url)
	fs.StringVar(&out, "out", "", "where to write the bulk-change receipt (required)")
	fs.StringVar(&prefix, "prefix", "bcdrill", "token prefix of everything the seed creates")
	fs.IntVar(&count, "count", 2000, "how many devices to create")
	fs.IntVar(&workers, "workers", 16, "concurrent attribute writes")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if out == "" || count < 1 || workers < 1 || !validToken(prefix) {
		return failWith(exitSetup, "--out, --count >= 1, --workers >= 1 and a token-shaped --prefix are required")
	}
	s, err := openSession(c, receipt, url)
	if err != nil {
		return err
	}
	b := bulkReceipt{Prefix: prefix, Profile: prefix + "-profile", DeviceType: prefix + "-type",
		ModeKey: prefix + bcModeSuffix, WarmKey: prefix + bcWarmSuffix}

	steps := []struct {
		what string
		doc  string
		vars map[string]any
	}{
		{"create the drill's device profile", bcCreateProfileDoc, map[string]any{"req": map[string]any{
			"token": b.Profile, "name": "bulk-change drill profile", "description": "Written by the upgrade drill's bulkchange phase."}}},
		{"declare the drill's configuration keys", bcDeclareDoc, map[string]any{"token": b.Profile, "keys": []any{
			map[string]any{"key": b.ModeKey, "valueType": bcValueType, "description": "The key the drill's bulk change sets."},
			map[string]any{"key": b.WarmKey, "valueType": bcValueType, "description": "Written on every drill device so each has a revision."},
		}}},
		{"publish the drill's device profile", bcPublishDoc, map[string]any{"token": b.Profile, "label": "bulk-change drill"}},
		{"create the drill's device type", bcCreateTypeDoc, map[string]any{"req": map[string]any{
			"token": b.DeviceType, "name": "bulk-change drill type", "profileToken": b.Profile}}},
	}
	for _, st := range steps {
		if err := s.query(ctx, st.doc, st.vars, nil); err != nil {
			return failWith(exitRefused, "%s: %w", st.what, err)
		}
	}
	for start := 1; start <= count; start += bcMaxPerBatch {
		n := min(bcMaxPerBatch, count-start+1)
		var got struct {
			CreateDevices []struct {
				Token string `json:"token"`
			} `json:"createDevices"`
		}
		err := s.query(ctx, bcCreateDevicesDoc, map[string]any{"req": map[string]any{
			"deviceTypeToken": b.DeviceType, "count": n, "startIndex": start,
			"tokenTemplate": prefix + "-{n:05d}", "nameTemplate": "bulk-change drill device {n}"}}, &got)
		if err != nil {
			return failWith(exitRefused, "create devices %d..%d: %w", start, start+n-1, err)
		}
		if len(got.CreateDevices) != n {
			return failWith(exitShape, "create devices %d..%d returned %d devices", start, start+n-1, len(got.CreateDevices))
		}
		for i, d := range got.CreateDevices {
			want := bcDeviceToken(prefix, start+i)
			if d.Token != want {
				return failWith(exitShape, "device %d came back as %q, want %q", start+i, d.Token, want)
			}
			b.Devices = append(b.Devices, d.Token)
		}
	}

	// Every device gets the warm key; three in four get the mode key too.
	type write struct{ token, key, value string }
	var writes []write
	for i, tok := range b.Devices {
		n := i + 1
		writes = append(writes, write{tok, b.WarmKey, fmt.Sprintf("warm-%d", n)})
		if bcModeSeeded(n) {
			writes = append(writes, write{tok, b.ModeKey, bcSeedValue(n)})
		}
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(writes) || ctx.Err() != nil {
					return
				}
				wr := writes[i]
				if _, err := bcSetAttr(ctx, s, wr.token, wr.key, wr.value); err != nil {
					errs <- fmt.Errorf("seed write %s %s=%s: %w", wr.token, wr.key, wr.value, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		return failWith(exitRefused, "%w", err)
	}
	body, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, body, 0o600); err != nil {
		return failWith(exitSetup, "write %s: %w", out, err)
	}
	fmt.Fprintf(stdout, "bulkchange seed: %d devices of %s, %d attribute writes, receipt %s\n", len(b.Devices), b.DeviceType, len(writes), out)
	return nil
}

func bcDeviceToken(prefix string, n int) string { return fmt.Sprintf("%s-%05d", prefix, n) }

// validToken is the shape the drill's own prefixes and tokens are held to before they reach a template.
func validToken(s string) bool {
	if s == "" || len(s) > 40 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// bcSetAttr writes one SHARED attribute, retrying ATTRIBUTE_WRITE_BUSY the way attrwrite does. It returns
// how many BUSY retries it took.
func bcSetAttr(ctx context.Context, s *bcSession, device, key, value string) (int, error) {
	vars := map[string]any{"req": map[string]any{
		"entityType": "device", "entity": device, "scope": probeAttrScope,
		"attrKey": key, "valueType": bcValueType, "value": value,
	}}
	return bcRetryBusy(ctx, func() error { return s.query(ctx, attrWriteMutation, vars, nil) })
}

func bcUnsetAttr(ctx context.Context, s *bcSession, device, key string) (int, error) {
	vars := map[string]any{"entityType": "device", "entity": device, "scope": probeAttrScope, "attrKey": key}
	return bcRetryBusy(ctx, func() error { return s.query(ctx, bcUnsetDoc, vars, nil) })
}

func bcRetryBusy(ctx context.Context, call func() error) (int, error) {
	retries := 0
	for try := 1; ; try++ {
		err := call()
		if err == nil || !userclient.AllHaveCode(err, codeAttributeWriteBusy) || try >= attrBusyMaxTries {
			return retries, err
		}
		retries++
		select {
		case <-ctx.Done():
			return retries, ctx.Err()
		case <-time.After(attrBusyBackoff()):
		}
	}
}

// ---------------------------------------------------------------------------
// start / status / abort / lines / delete
// ---------------------------------------------------------------------------

func bcStart(ctx context.Context, argv []string, stdout io.Writer) error {
	fs := flagSetFor("bulkchange start")
	var c connection
	c.bind(fs)
	var receipt, url, bulk, token, value, devicesFile string
	var first, last, rate, deadline int
	bindSession(fs, &receipt, &url)
	fs.StringVar(&bulk, "bulk", "", "the bulk-change receipt seed wrote (required)")
	fs.StringVar(&devicesFile, "devices-file", "", "target exactly these device tokens, in this order, instead of --first..--last")
	fs.StringVar(&token, "token", "", "token of the bulk change (required)")
	fs.StringVar(&value, "value", "", "the value the change sets on the mode key (required)")
	fs.IntVar(&first, "first", 1, "first device (1-based) to target")
	fs.IntVar(&last, "last", 0, "last device to target (default: every device)")
	fs.IntVar(&rate, "rate", 50, "maxPostsPerSecond (the contract's ceiling is 50)")
	fs.IntVar(&deadline, "settle-deadline", 3600, "settleDeadlineSeconds")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if !validToken(token) || value == "" {
		return failWith(exitSetup, "a token-shaped --token and a --value are required")
	}
	b, err := readBulkReceipt(bulk)
	if err != nil {
		return err
	}
	if last == 0 {
		last = len(b.Devices)
	}
	if first < 1 || last > len(b.Devices) || first > last {
		return failWith(exitSetup, "--first %d / --last %d is outside 1..%d", first, last, len(b.Devices))
	}
	s, err := openSession(c, receipt, url)
	if err != nil {
		return err
	}
	devices := make([]any, 0, last-first+1)
	if devicesFile != "" {
		tokens, err := readTokens(devicesFile)
		if err != nil {
			return err
		}
		if len(tokens) == 0 {
			return failWith(exitSetup, "%s names no device", devicesFile)
		}
		for _, d := range tokens {
			devices = append(devices, d)
		}
	} else {
		for _, d := range b.Devices[first-1 : last] {
			devices = append(devices, d)
		}
	}
	request := map[string]any{
		"token": token, "name": "bulk-change drill " + token, "kind": "config.set",
		"configSet":    map[string]any{"key": b.ModeKey, "value": value, "valueType": bcValueType},
		"deviceTokens": devices,
		"safety":       map[string]any{"maxPostsPerSecond": rate, "settleDeadlineSeconds": deadline},
	}
	var plan struct {
		Plan struct {
			AdmittedCount  int    `json:"admittedCount"`
			RefusedCount   int    `json:"refusedCount"`
			ContractDigest string `json:"contractDigest"`
		} `json:"plan"`
	}
	if err := s.query(ctx, bcPlanDoc, map[string]any{"request": request}, &plan); err != nil {
		return failWith(exitRefused, "plan %s: %w", token, err)
	}
	if plan.Plan.AdmittedCount != len(devices) || plan.Plan.RefusedCount != 0 {
		return failWith(exitMismatch, "plan %s admitted %d and refused %d of %d existing devices", token,
			plan.Plan.AdmittedCount, plan.Plan.RefusedCount, len(devices))
	}
	var created struct {
		Change bcChange `json:"change"`
	}
	if err := s.query(ctx, bcCreateDoc, map[string]any{"request": request}, &created); err != nil {
		return failWith(exitRefused, "create %s: %w", token, err)
	}
	if created.Change.Dispatch != "READY" || created.Change.TargetCount != len(devices) {
		return failWith(exitMismatch, "create %s answered dispatch %s with %d targets, want READY with %d", token,
			created.Change.Dispatch, created.Change.TargetCount, len(devices))
	}
	// The digest presented is the one the plan showed: what a person reviewed. A create that moved it is
	// refused at start, which is the start's own check and is left to it.
	var started struct {
		Change bcChange `json:"change"`
	}
	if err := s.query(ctx, bcStartDoc, map[string]any{"token": token, "digest": plan.Plan.ContractDigest}, &started); err != nil {
		return failWith(exitRefused, "start %s: %w", token, err)
	}
	if started.Change.Dispatch != "RUNNING" {
		return failWith(exitMismatch, "start %s answered dispatch %s, want RUNNING", token, started.Change.Dispatch)
	}
	return json.NewEncoder(stdout).Encode(started.Change)
}

func bcStatusCmd(ctx context.Context, argv []string, stdout io.Writer, doc string) error {
	fs := flagSetFor("bulkchange status")
	var c connection
	c.bind(fs)
	var receipt, url, token string
	bindSession(fs, &receipt, &url)
	fs.StringVar(&token, "token", "", "token of the bulk change (required)")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	s, err := openSession(c, receipt, url)
	if err != nil {
		return err
	}
	var out struct {
		Change *bcChange `json:"change"`
	}
	if err := s.query(ctx, doc, map[string]any{"token": token}, &out); err != nil {
		return failWith(exitRefused, "bulk change %s: %w", token, err)
	}
	if out.Change == nil {
		return failWith(exitMissing, "no bulk change %s", token)
	}
	return json.NewEncoder(stdout).Encode(out.Change)
}

func bcLinesCmd(ctx context.Context, argv []string, stdout io.Writer) error {
	fs := flagSetFor("bulkchange lines")
	var c connection
	c.bind(fs)
	var receipt, url, token, out string
	bindSession(fs, &receipt, &url)
	fs.StringVar(&token, "token", "", "token of the bulk change (required)")
	fs.StringVar(&out, "out", "", "file to write every line to, as a JSON array (required)")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if out == "" {
		return failWith(exitSetup, "--out is required")
	}
	s, err := openSession(c, receipt, url)
	if err != nil {
		return err
	}
	lines, err := bcAllLines(ctx, s, token)
	if err != nil {
		return err
	}
	body, err := json.Marshal(lines)
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, body, 0o600); err != nil {
		return failWith(exitSetup, "write %s: %w", out, err)
	}
	fmt.Fprintf(stdout, "bulkchange lines: %d line(s) of %s to %s\n", len(lines), token, out)
	return nil
}

func bcAllLines(ctx context.Context, s *bcSession, token string) ([]bcLine, error) {
	var all []bcLine
	for page := 1; ; page++ {
		var got struct {
			Lines struct {
				Results    []bcLine `json:"results"`
				Pagination struct {
					TotalRecords int `json:"totalRecords"`
				} `json:"pagination"`
			} `json:"lines"`
		}
		vars := map[string]any{"token": token, "criteria": map[string]any{"pageNumber": page, "pageSize": bcLinesPage}}
		if err := s.query(ctx, bcLinesDoc, vars, &got); err != nil {
			return nil, failWith(exitRefused, "lines of %s, page %d: %w", token, page, err)
		}
		all = append(all, got.Lines.Results...)
		if len(got.Lines.Results) < bcLinesPage || len(all) >= got.Lines.Pagination.TotalRecords {
			if len(all) != got.Lines.Pagination.TotalRecords {
				return nil, failWith(exitShape, "read %d line(s) of %s but the server counts %d", len(all), token, got.Lines.Pagination.TotalRecords)
			}
			return all, nil
		}
		if page > 1000 {
			return nil, failWith(exitShape, "the lines of %s never ended", token)
		}
	}
}

func bcDelete(ctx context.Context, argv []string, stdout io.Writer) error {
	fs := flagSetFor("bulkchange delete")
	var c connection
	c.bind(fs)
	var receipt, url, device string
	bindSession(fs, &receipt, &url)
	fs.StringVar(&device, "device", "", "token of the device to delete (required)")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	s, err := openSession(c, receipt, url)
	if err != nil {
		return err
	}
	var out struct {
		DeleteDevice bool `json:"deleteDevice"`
	}
	if err := s.query(ctx, bcDeleteDeviceDoc, map[string]any{"token": device}, &out); err != nil {
		return failWith(exitRefused, "delete %s through %s: %w", device, s.url, err)
	}
	if !out.DeleteDevice {
		return failWith(exitMissing, "delete %s through %s answered false: no such device", device, s.url)
	}
	fmt.Fprintf(stdout, "bulkchange delete: %s deleted through %s\n", device, s.url)
	return nil
}

// ---------------------------------------------------------------------------
// oldwrite — ordinary attribute writes through a BASELINE pod while the change runs
// ---------------------------------------------------------------------------

// bcWriteResult is what the writers did. Every write is classified by what the pod ANSWERED: accepted
// (possibly after BUSY retries) or refused (any other error, or BUSY past its retry bound).
type bcWriteResult struct {
	Attempted   int64    `json:"attempted"`
	Accepted    int64    `json:"accepted"`
	Refused     int64    `json:"refused"`
	ModeWrites  int64    `json:"modeWrites"`
	FirstWrites int64    `json:"firstWritesOfAbsentKey"`
	Unsets      int64    `json:"unsets"`
	BusyRetries int64    `json:"busyRetries"`
	Errors      []string `json:"firstErrors"`
	Seconds     float64  `json:"seconds"`
}

// bcWriteVerdict judges a writer run against what it was expected to see. Pure.
//
//	accepted  every write was accepted (the supported roll: a baseline pod that marks its writes)
//	refused   every write was refused (a baseline from before the marker: the guard refuses it)
//	any       nothing is required of the answers, only that writes were made (a control's setup)
//
// In every mode a run that made no write, or made no write of the mode key, says nothing and fails.
func bcWriteVerdict(r bcWriteResult, expect string) error {
	if r.Attempted == 0 || r.ModeWrites == 0 {
		return fmt.Errorf("the writers made %d write(s), %d of the mode key: a run that wrote nothing proves nothing", r.Attempted, r.ModeWrites)
	}
	if r.Accepted+r.Refused != r.Attempted {
		return fmt.Errorf("the writers counted %d attempted but %d accepted and %d refused", r.Attempted, r.Accepted, r.Refused)
	}
	switch expect {
	case "accepted":
		if r.Refused != 0 {
			return fmt.Errorf("%d of %d write(s) were REFUSED, first: %s", r.Refused, r.Attempted, strings.Join(r.Errors, " | "))
		}
		if r.FirstWrites == 0 {
			return fmt.Errorf("no write was a first write of an absent key, which the drill requires")
		}
	case "refused":
		if r.Accepted != 0 {
			return fmt.Errorf("%d of %d write(s) were ACCEPTED where every one must be refused", r.Accepted, r.Attempted)
		}
	case "any":
	default:
		return fmt.Errorf("unknown expectation %q", expect)
	}
	return nil
}

func readTokens(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, failWith(exitSetup, "open %s: %w", path, err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if t := strings.TrimSpace(sc.Text()); t != "" {
			out = append(out, t)
		}
	}
	return out, sc.Err()
}

func bcOldWrite(ctx context.Context, argv []string, stdout io.Writer) error {
	fs := flagSetFor("bulkchange oldwrite")
	var c connection
	c.bind(fs)
	var receipt, url, bulk, modeFile, warmFile, expect, stopFile, out string
	var workers int
	var maxDur time.Duration
	var toggle bool
	bindSession(fs, &receipt, &url)
	fs.StringVar(&bulk, "bulk", "", "the bulk-change receipt seed wrote (required)")
	fs.StringVar(&modeFile, "mode-devices", "", "file of device tokens whose MODE key is written (required)")
	fs.StringVar(&warmFile, "warm-devices", "", "file of device tokens whose WARM key is written")
	fs.StringVar(&expect, "expect", "accepted", "accepted | refused | any")
	fs.StringVar(&stopFile, "stop-file", "", "stop when this file exists (required)")
	fs.StringVar(&out, "out", "", "write the result JSON here (required)")
	fs.IntVar(&workers, "workers", 8, "concurrent writers")
	fs.DurationVar(&maxDur, "max-duration", 20*time.Minute, "stop after this long even without the stop file")
	fs.BoolVar(&toggle, "toggle", false, "alternate unset and set of the mode key, so it is absent half the time (the H01 race)")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if modeFile == "" || stopFile == "" || out == "" || workers < 1 {
		return failWith(exitSetup, "--mode-devices, --stop-file, --out and --workers >= 1 are required")
	}
	b, err := readBulkReceipt(bulk)
	if err != nil {
		return err
	}
	modeDevs, err := readTokens(modeFile)
	if err != nil {
		return err
	}
	var warmDevs []string
	if warmFile != "" {
		if warmDevs, err = readTokens(warmFile); err != nil {
			return err
		}
	}
	if len(modeDevs) == 0 {
		return failWith(exitSetup, "%s names no device", modeFile)
	}
	s, err := openSession(c, receipt, url)
	if err != nil {
		return err
	}
	seeded := map[string]bool{}
	for i, d := range b.Devices {
		seeded[d] = bcModeSeeded(i + 1)
	}

	type job struct {
		token string
		mode  bool
	}
	var jobs []job
	for _, d := range modeDevs {
		jobs = append(jobs, job{d, true})
	}
	for _, d := range warmDevs {
		jobs = append(jobs, job{d, false})
	}
	var res bcWriteResult
	var mu sync.Mutex
	present := map[string]bool{} // what THIS writer last left on each mode device
	for _, d := range modeDevs {
		present[d] = seeded[d]
	}
	note := func(err error, retries int, mode, first, unset bool) {
		mu.Lock()
		defer mu.Unlock()
		res.Attempted++
		res.BusyRetries += int64(retries)
		if mode {
			res.ModeWrites++
		}
		if err != nil {
			res.Refused++
			if len(res.Errors) < 5 {
				res.Errors = append(res.Errors, err.Error())
			}
			return
		}
		res.Accepted++
		if first {
			res.FirstWrites++
		}
		if unset {
			res.Unsets++
		}
	}
	started := time.Now()
	deadline := started.Add(maxDur)
	var seq atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := w; ; i += workers {
				if ctx.Err() != nil || time.Now().After(deadline) {
					return
				}
				if _, err := os.Stat(stopFile); err == nil {
					return
				}
				j := jobs[i%len(jobs)]
				n := seq.Add(1)
				if !j.mode {
					retries, err := bcSetAttr(ctx, s, j.token, b.WarmKey, fmt.Sprintf("old-warm-%d", n))
					note(err, retries, false, false, false)
					continue
				}
				mu.Lock()
				had := present[j.token]
				mu.Unlock()
				if toggle && had {
					retries, err := bcUnsetAttr(ctx, s, j.token, b.ModeKey)
					if err == nil {
						mu.Lock()
						present[j.token] = false
						mu.Unlock()
					}
					note(err, retries, true, false, true)
					continue
				}
				retries, err := bcSetAttr(ctx, s, j.token, b.ModeKey, fmt.Sprintf("old-%d", n))
				if err == nil {
					mu.Lock()
					present[j.token] = true
					mu.Unlock()
				}
				note(err, retries, true, !had, false)
			}
		}(w)
	}
	wg.Wait()
	res.Seconds = time.Since(started).Seconds()
	body, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, body, 0o600); err != nil {
		return failWith(exitSetup, "write %s: %w", out, err)
	}
	fmt.Fprintf(stdout, "bulkchange oldwrite: %d attempted, %d accepted, %d refused, %d first write(s) of an absent key, %d unset(s), %d BUSY retries, %.0fs, through %s\n",
		res.Attempted, res.Accepted, res.Refused, res.FirstWrites, res.Unsets, res.BusyRetries, res.Seconds, url)
	if err := bcWriteVerdict(res, expect); err != nil {
		return failWith(exitMismatch, "writes through %s (expected %s): %w", url, expect, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// The verdicts. Pure, and each is handed the states it must refuse in bulkchange_test.go.
// ---------------------------------------------------------------------------

// bcVerdict is what judgeLines found.
type bcVerdict struct {
	Problems    []string `json:"problems"`
	Lines       int      `json:"lines"`
	Settled     int      `json:"settled"`
	ReportLog   int      `json:"reportLog"`
	LatestState int      `json:"latestState"`
	Confirmed   int      `json:"confirmed"`
	Superseded  int      `json:"superseded"`
	Erased      int      `json:"erased"`
}

func strOr(p *string) string {
	if p == nil {
		return "<null>"
	}
	return *p
}

// bcLineRedacted says whether one line shows every sign of erasure the API exposes: flagged, no target
// token, no prior, no posted value.
func bcLineRedacted(l bcLine) bool {
	return l.Redacted && l.TargetToken == nil && l.Prior == nil && l.PostedValue == nil
}

// judgeRedaction returns one problem per given ordinal whose line is not redacted (or absent).
func judgeRedaction(lines []bcLine, ordinals []int) []string {
	by := map[int]bcLine{}
	for _, l := range lines {
		by[l.Ordinal] = l
	}
	var problems []string
	for _, o := range ordinals {
		l, ok := by[o]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("line %d is missing", o))
		case !bcLineRedacted(l):
			problems = append(problems, fmt.Sprintf("line %d of a deleted device is not redacted (redacted=%v targetToken=%s prior=%s)",
				o, l.Redacted, strOr(l.TargetToken), strOr(l.Prior)))
		}
	}
	return problems
}

// judgeLines is the drill's verdict on a finished bulk change of count lines, ordinals 1..count, where the
// devices at the deleted ordinals were deleted while it ran:
//
//   - every ordinal is present exactly once;
//   - a deleted device's line is redacted, and is either SKIPPED (it never posted) or POSTED and settled
//     AMBIGUOUS with TARGET_ERASED;
//   - every other line POSTED and settled from evidence: CONFIRMED or SUPERSEDED (the drill's devices
//     report APPLIED, so FAILED and AMBIGUOUS are findings), with a basis;
//   - REPORT_LOG plus LATEST_STATE equals the evidence-settled total;
//   - with wantReportLog (capture shipped in the baseline) every basis is REPORT_LOG.
func judgeLines(lines []bcLine, count int, deleted map[int]bool, wantReportLog bool) bcVerdict {
	v := bcVerdict{Lines: len(lines)}
	add := func(f string, a ...any) {
		if len(v.Problems) < 25 {
			v.Problems = append(v.Problems, fmt.Sprintf(f, a...))
		}
	}
	seen := map[int]int{}
	for _, l := range lines {
		seen[l.Ordinal]++
	}
	for o := 1; o <= count; o++ {
		if seen[o] != 1 {
			add("ordinal %d appears %d time(s)", o, seen[o])
		}
	}
	if len(lines) != count {
		add("%d line(s), want %d", len(lines), count)
	}
	for _, l := range lines {
		settlement := strOr(l.Settlement)
		if deleted[l.Ordinal] {
			if !bcLineRedacted(l) {
				add("line %d of a deleted device is not redacted", l.Ordinal)
			}
			switch {
			case l.Posting == "SKIPPED":
			case l.Posting == "POSTED" && settlement == "AMBIGUOUS" && strOr(l.Code) == "TARGET_ERASED":
			default:
				add("line %d of a deleted device is %s/%s/%s, want SKIPPED or POSTED/AMBIGUOUS/TARGET_ERASED", l.Ordinal, l.Posting, settlement, strOr(l.Code))
			}
			v.Erased++
			continue
		}
		if l.Redacted {
			add("line %d is redacted but its device was not deleted", l.Ordinal)
		}
		if l.Posting != "POSTED" {
			add("line %d did not post: %s (%s)", l.Ordinal, l.Posting, strOr(l.Code))
			continue
		}
		basis := strOr(l.EvidenceBasis)
		switch settlement {
		case "CONFIRMED", "SUPERSEDED", "FAILED":
			v.Settled++
			if settlement == "CONFIRMED" {
				v.Confirmed++
			}
			if settlement == "SUPERSEDED" {
				v.Superseded++
			}
			if settlement == "FAILED" {
				add("line %d settled FAILED, but its device reported the revision APPLIED", l.Ordinal)
			}
		default:
			add("line %d did not settle from evidence: settlement %s", l.Ordinal, settlement)
			continue
		}
		switch basis {
		case "REPORT_LOG":
			v.ReportLog++
		case "LATEST_STATE":
			v.LatestState++
			if wantReportLog {
				add("line %d settled on LATEST_STATE, but the baseline captures every report, so every line must read REPORT_LOG", l.Ordinal)
			}
		default:
			add("line %d settled %s with no evidence basis (%s)", l.Ordinal, settlement, basis)
		}
	}
	if v.ReportLog+v.LatestState != v.Settled {
		add("REPORT_LOG (%d) + LATEST_STATE (%d) != settled (%d)", v.ReportLog, v.LatestState, v.Settled)
	}
	if v.Settled == 0 {
		add("no line settled from evidence, so the settlement checks above judged nothing")
	}
	return v
}

// bcImage is the before- or after-image a line records.
type bcImage struct {
	Present   bool    `json:"present"`
	ValueType string  `json:"valueType"`
	Value     *string `json:"value"`
}

// bcPriorRow is one POSTED line beside the configuration documents its device held: the one its own post
// minted (at) and the newest one before it (before; "" when the device had none).
type bcPriorRow struct {
	Ordinal   int
	Prior     string
	Posted    string
	DocAt     string
	DocBefore string
}

// keyIn reads key out of a configuration document: (value, true) for a STRING present, ("", false) when
// absent. A document that does not parse, or a value that is not a string, is an error.
func keyIn(doc, key string) (string, bool, error) {
	if doc == "" {
		return "", false, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		return "", false, fmt.Errorf("document does not parse: %w", err)
	}
	raw, ok := m[key]
	if !ok {
		return "", false, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false, fmt.Errorf("%s is not a string in the document: %s", key, raw)
	}
	return s, true, nil
}

// judgePrior decides one line. The configuration revisions are an INDEPENDENT record of what the device
// held: a write of a declared key mints a revision in its own transaction, every writer serialises minting
// on the device's configuration row, and the post's own write minted the revision the line names. So the
// line's prior must be the key as the document just before that revision held it.
//
// unjudgeable is true when the documents cannot say: the post's own revision does not carry the posted
// value (the revision attribution the check rests on does not hold, so a "correct" would mean nothing).
func judgePrior(key string, r bcPriorRow) (ok bool, why string, unjudgeable bool) {
	var prior, posted bcImage
	if err := json.Unmarshal([]byte(r.Prior), &prior); err != nil {
		return false, fmt.Sprintf("line %d: prior does not parse: %v", r.Ordinal, err), true
	}
	if err := json.Unmarshal([]byte(r.Posted), &posted); err != nil || !posted.Present || posted.Value == nil {
		return false, fmt.Sprintf("line %d: posted value does not parse or is not a set", r.Ordinal), true
	}
	at, atOK, err := keyIn(r.DocAt, key)
	if err != nil || !atOK || at != *posted.Value {
		return false, fmt.Sprintf("line %d: the revision its post minted holds %s=%q (present %v), not the posted %q",
			r.Ordinal, key, at, atOK, *posted.Value), true
	}
	before, beforeOK, err := keyIn(r.DocBefore, key)
	if err != nil {
		return false, fmt.Sprintf("line %d: %v", r.Ordinal, err), true
	}
	switch {
	case !beforeOK && !prior.Present:
		return true, "", false
	case !beforeOK && prior.Present:
		return false, fmt.Sprintf("line %d: prior says %q, but the device held no %s before the post", r.Ordinal, strOr(prior.Value), key), false
	case beforeOK && !prior.Present:
		return false, fmt.Sprintf("line %d: prior says ABSENT, but the device held %s=%q before the post", r.Ordinal, key, before), false
	case prior.Value == nil || *prior.Value != before:
		return false, fmt.Sprintf("line %d: prior says %q, but the device held %s=%q before the post", r.Ordinal, strOr(prior.Value), key, before), false
	}
	return true, "", false
}

// bcPriorSummary totals judgePrior over every row.
type bcPriorSummary struct {
	Rows        int      `json:"rows"`
	Correct     int      `json:"correct"`
	Wrong       int      `json:"wrong"`
	Unjudgeable int      `json:"unjudgeable"`
	Examples    []string `json:"examples"`
}

func judgePriors(key string, rows []bcPriorRow) bcPriorSummary {
	s := bcPriorSummary{Rows: len(rows)}
	for _, r := range rows {
		ok, why, unj := judgePrior(key, r)
		switch {
		case unj:
			s.Unjudgeable++
		case ok:
			s.Correct++
		default:
			s.Wrong++
		}
		if why != "" && len(s.Examples) < 10 {
			s.Examples = append(s.Examples, why)
		}
	}
	return s
}

// parsePriorRows reads the rig's psql export: ordinal, prior, posted value, document at, document before,
// separated by a unit separator (0x1f) so JSON cannot collide with it; an empty last field is "no document".
func parsePriorRows(r io.Reader) ([]bcPriorRow, error) {
	var rows []bcPriorRow
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		f := strings.Split(line, "\x1f")
		if len(f) != 5 {
			return nil, fmt.Errorf("a row has %d field(s), want 5: %.80q", len(f), line)
		}
		o, err := strconv.Atoi(f[0])
		if err != nil {
			return nil, fmt.Errorf("ordinal %q: %w", f[0], err)
		}
		rows = append(rows, bcPriorRow{Ordinal: o, Prior: f[1], Posted: f[2], DocAt: f[3], DocBefore: f[4]})
	}
	return rows, sc.Err()
}

func parseOrdinals(s string) ([]int, error) {
	var out []int
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("ordinal %q is not a positive integer", p)
		}
		out = append(out, n)
	}
	sort.Ints(out)
	return out, nil
}

func readLines(path string) ([]bcLine, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, failWith(exitSetup, "read %s: %w", path, err)
	}
	var lines []bcLine
	if err := json.Unmarshal(body, &lines); err != nil {
		return nil, failWith(exitShape, "%s is not a JSON array of lines: %w", path, err)
	}
	return lines, nil
}

func bcJudgeCmd(argv []string, stdout io.Writer) error {
	fs := flagSetFor("bulkchange judge")
	var linesFile, deleted string
	var count int
	var reportLog bool
	fs.StringVar(&linesFile, "lines", "", "the lines file `bulkchange lines` wrote (required)")
	fs.IntVar(&count, "count", 0, "how many lines the change has (required)")
	fs.StringVar(&deleted, "deleted", "", "comma-separated ordinals whose devices were deleted")
	fs.BoolVar(&reportLog, "report-log", false, "require every settled line to read REPORT_LOG")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	ords, err := parseOrdinals(deleted)
	if err != nil || count < 1 {
		return failWith(exitSetup, "--count >= 1 and well-formed --deleted are required: %v", err)
	}
	lines, err := readLines(linesFile)
	if err != nil {
		return err
	}
	del := map[int]bool{}
	for _, o := range ords {
		del[o] = true
	}
	v := judgeLines(lines, count, del, reportLog)
	if err := json.NewEncoder(stdout).Encode(v); err != nil {
		return err
	}
	if len(v.Problems) != 0 {
		return failWith(exitMismatch, "the bulk change did not finish as required: %s", strings.Join(v.Problems, "; "))
	}
	return nil
}

func bcRedactedCmd(argv []string, stdout io.Writer) error {
	fs := flagSetFor("bulkchange redacted")
	var linesFile, ordinals, expect string
	fs.StringVar(&linesFile, "lines", "", "the lines file `bulkchange lines` wrote (required)")
	fs.StringVar(&ordinals, "ordinals", "", "comma-separated ordinals whose devices were deleted (required)")
	fs.StringVar(&expect, "expect", "redacted", "redacted | unredacted (the repair-disabled control)")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	ords, err := parseOrdinals(ordinals)
	if err != nil || len(ords) == 0 {
		return failWith(exitSetup, "--ordinals must name at least one ordinal: %v", err)
	}
	lines, err := readLines(linesFile)
	if err != nil {
		return err
	}
	problems := judgeRedaction(lines, ords)
	switch expect {
	case "redacted":
		if len(problems) != 0 {
			return failWith(exitMismatch, "%s", strings.Join(problems, "; "))
		}
		fmt.Fprintf(stdout, "bulkchange redacted: line(s) %v redacted\n", ords)
	case "unredacted":
		if len(problems) != len(ords) {
			return failWith(exitMismatch, "only %d of %d line(s) are unredacted, so the redaction check would not have failed", len(problems), len(ords))
		}
		fmt.Fprintf(stdout, "bulkchange redacted: line(s) %v are NOT redacted, so the check fails here\n", ords)
	default:
		return failWith(exitSetup, "unknown --expect %q", expect)
	}
	return nil
}

func bcPriorsCmd(argv []string, stdout io.Writer) error {
	fs := flagSetFor("bulkchange priors")
	var rowsFile, key, expect string
	var minRows int
	fs.StringVar(&rowsFile, "rows", "", "the rig's export of posted lines beside their documents (required)")
	fs.StringVar(&key, "key", "", "the configuration key the change set (required)")
	fs.StringVar(&expect, "expect", "correct", "correct | wrong (the before-image control: at least one wrong)")
	fs.IntVar(&minRows, "min-rows", 1, "fewer rows than this judges too little")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if rowsFile == "" || key == "" {
		return failWith(exitSetup, "--rows and --key are required")
	}
	f, err := os.Open(rowsFile)
	if err != nil {
		return failWith(exitSetup, "open %s: %w", rowsFile, err)
	}
	defer f.Close()
	rows, err := parsePriorRows(f)
	if err != nil {
		return failWith(exitShape, "%s: %w", rowsFile, err)
	}
	s := judgePriors(key, rows)
	if err := json.NewEncoder(stdout).Encode(s); err != nil {
		return err
	}
	if s.Rows < minRows {
		return failWith(exitShape, "%d posted line(s) to judge, fewer than %d", s.Rows, minRows)
	}
	if s.Unjudgeable != 0 {
		return failWith(exitShape, "%d line(s) could not be judged, so no verdict is offered: %s", s.Unjudgeable, strings.Join(s.Examples, "; "))
	}
	switch expect {
	case "correct":
		if s.Wrong != 0 {
			return failWith(exitMismatch, "%d of %d recorded prior(s) are WRONG: %s", s.Wrong, s.Rows, strings.Join(s.Examples, "; "))
		}
	case "wrong":
		if s.Wrong == 0 {
			return failWith(exitMismatch, "every one of %d prior(s) is correct, so the before-image check did not fail", s.Rows)
		}
	default:
		return failWith(exitSetup, "unknown --expect %q", expect)
	}
	return nil
}

// bcLockKey prints the advisory-lock key a singleton pass of the given name takes, from the platform's OWN
// derivation (rdb.AdvisoryLockKey), so the rig's repair-disabled control holds exactly the lock the
// maintenance pass tries and never a re-implementation of the hash.
func bcLockKey(argv []string, stdout io.Writer) error {
	fs := flagSetFor("bulkchange lockkey")
	var name string
	fs.StringVar(&name, "name", "", "the singleton pass's lock name (required)")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if name == "" {
		return failWith(exitSetup, "--name is required")
	}
	_, err := fmt.Fprintln(stdout, rdb.AdvisoryLockKey(name))
	return err
}

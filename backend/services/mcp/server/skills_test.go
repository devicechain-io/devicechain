// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- the served projection ----

// skillSession connects a real client through New's handler, under a token for tenant
// with the given scopes, with the proposal tools switched on or off, and counts every
// request that reaches the GraphQL upstream.
func skillSession(t *testing.T, tenant, scopes string, actuation bool) (*mcp.ClientSession, *atomic.Int64) {
	t.Helper()
	hits := &atomic.Int64{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	t.Cleanup(up.Close)
	iss, validator := mustIssuerValidator(t)
	handler, _ := New(testResource, "https://as.example.com", validator, testClient(up.URL), DefaultLimits(),
		WithActuation(actuation), WithCommandProposals(actuation))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	tok, err := iss.IssueOAuthAccess(tenant, "a@"+tenant+".example", []string{"viewer"},
		[]string{"device:read", "alarm:propose", "command:propose"}, scopes, []string{testResource}, false, "client-"+tenant, "j-skills-"+tenant)
	if err != nil {
		t.Fatalf("issuing a token: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint:             ts.URL,
			HTTPClient:           &http.Client{Transport: bearerTransport{token: tok.Token}},
			MaxRetries:           -1,
			DisableStandaloneSSE: true,
		}, nil)
	if err != nil {
		t.Fatalf("client connect through the served handler: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, hits
}

// servedSkillCatalog reads everything the skill projections serve, in a fixed order, as
// one string: the instructions, the prompt and resource listings, every prompt and every
// resource. It is what the static test compares between callers.
func servedSkillCatalog(t *testing.T, cs *mcp.ClientSession) string {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder
	write := func(label string, v any) {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %s: %v", label, err)
		}
		fmt.Fprintf(&b, "%s %s\n", label, raw)
	}
	write("instructions", cs.InitializeResult().Instructions)
	prompts, err := cs.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatalf("prompts/list: %v", err)
	}
	write("prompts", prompts.Prompts)
	for _, p := range prompts.Prompts {
		got, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: p.Name})
		if err != nil {
			t.Fatalf("prompts/get %s: %v", p.Name, err)
		}
		write("prompt "+p.Name, got)
	}
	resources, err := cs.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("resources/list: %v", err)
	}
	write("resources", resources.Resources)
	for _, r := range resources.Resources {
		got, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: r.URI})
		if err != nil {
			t.Fatalf("resources/read %s: %v", r.URI, err)
		}
		write("resource "+r.URI, got.Contents)
	}
	return b.String()
}

// The wire test: what a client reads through New's own handler is, byte for byte, the
// embedded file; every prompt returns its SKILL.md; the listings hold exactly the
// catalog and nothing else; and none of it reaches the GraphQL upstream.
func TestServedSkillsAreTheEmbeddedFilesAndNothingElse(t *testing.T) {
	ctx := context.Background()
	cs, hits := skillSession(t, "acme", coreauth.ScopeReadOnly, false)

	if got := cs.InitializeResult().Instructions; got != serverInstructions {
		t.Fatalf("initialize instructions = %q, want serverInstructions", got)
	}

	wantFiles := map[string]SkillFile{}
	for _, skill := range platformSkills {
		for _, f := range skill.Files {
			wantFiles[f.URI] = f
		}
	}
	resources, err := cs.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("resources/list: %v", err)
	}
	served := map[string]bool{}
	for _, r := range resources.Resources {
		served[r.URI] = true
		want, ok := wantFiles[r.URI]
		if !ok {
			t.Errorf("resources/list serves %s, which is not a skill file: every resource goes through registerSkills", r.URI)
			continue
		}
		if r.MIMEType != skillMIMEType || r.Size != int64(len(want.Body)) {
			t.Errorf("%s listed as %s, %d bytes; want %s, %d bytes", r.URI, r.MIMEType, r.Size, skillMIMEType, len(want.Body))
		}
		meta, _ := r.Meta[skillMetaKey].(map[string]any)
		if meta["digest"] != want.Digest {
			t.Errorf("%s listed with digest %v, want %s", r.URI, meta["digest"], want.Digest)
		}
		got, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: r.URI})
		if err != nil {
			t.Fatalf("resources/read %s: %v", r.URI, err)
		}
		if len(got.Contents) != 1 || got.Contents[0].Text != string(want.Body) || got.Contents[0].MIMEType != skillMIMEType {
			t.Errorf("resources/read %s did not return the embedded file's bytes", r.URI)
			continue
		}
		sum := sha256.Sum256([]byte(got.Contents[0].Text))
		if "sha256:"+hex.EncodeToString(sum[:]) != want.Digest {
			t.Errorf("%s served bytes do not match the digest it is listed with", r.URI)
		}
	}
	for uri := range wantFiles {
		if !served[uri] {
			t.Errorf("skill file %s is not served as a resource", uri)
		}
	}

	prompts, err := cs.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatalf("prompts/list: %v", err)
	}
	var names, want []string
	for _, p := range prompts.Prompts {
		names = append(names, p.Name)
	}
	for _, skill := range platformSkills {
		want = append(want, skill.Name)
		got, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: skill.Name})
		if err != nil {
			t.Fatalf("prompts/get %s: %v", skill.Name, err)
		}
		if len(got.Messages) != 1 {
			t.Fatalf("prompt %s has %d messages, want 1", skill.Name, len(got.Messages))
		}
		text, ok := got.Messages[0].Content.(*mcp.TextContent)
		if !ok || text.Text != string(skill.SkillMD().Body) || got.Messages[0].Role != "user" {
			t.Errorf("prompt %s does not return its SKILL.md as one user message", skill.Name)
		}
	}
	sort.Strings(names)
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("prompts/list = %v, want exactly the skills %v", names, want)
	}

	if _, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "skill://alarm-storm-triage/scripts/run.sh"}); err == nil {
		t.Errorf("reading a URI outside the catalog succeeded")
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("reading skills made %d requests to the GraphQL upstream; a skill read holds no authority and makes none", n)
	}
}

// Static: the catalog is the same bytes for every caller. Two callers in different
// tenants, one read-only and one with every action scope on a server with the proposal
// tools switched on, read identical instructions, listings, prompts and files.
func TestSkillCatalogIsTheSameForEveryCaller(t *testing.T) {
	readOnly, _ := skillSession(t, "acme", coreauth.ScopeReadOnly, false)
	actor, _ := skillSession(t, "globex", "read-only act:alarms act:commands location", true)
	a, b := servedSkillCatalog(t, readOnly), servedSkillCatalog(t, actor)
	if a != b {
		t.Fatalf("two callers were served different skill catalogs:\n--- acme read-only ---\n%s\n--- globex with action scopes ---\n%s", a, b)
	}
	if !strings.Contains(a, "skill://alarm-storm-triage/SKILL.md") {
		t.Fatalf("the snapshot holds no skill at all; the comparison above compared nothing")
	}
}

// No authority: a skill read is behind exactly the admission a tool call is. No bearer
// is a 401 challenge; a bearer without read-only is a 403.
func TestSkillReadsAreAdmittedLikeToolCalls(t *testing.T) {
	iss, validator := mustIssuerValidator(t)
	handler, _ := New(testResource, "https://as.example.com", validator, testClient("http://unused.invalid"), DefaultLimits())
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	read := `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"skill://alarm-storm-triage/SKILL.md"}}`
	prompt := `{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"alarm-storm-triage"}}`
	for _, body := range []string{read, prompt} {
		resp, _ := post(t, ts, "", body)
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(resp.Header.Get("WWW-Authenticate"), "resource_metadata") {
			t.Errorf("unauthenticated %s: status %d, challenge %q; want the 401 challenge a tool call gets", body, resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
		}
		tok, err := iss.IssueOAuthAccess("acme", "a@b.c", []string{"viewer"}, []string{"device:read"}, "location",
			[]string{testResource}, false, "mcp", "j-skills-noscope")
		if err != nil {
			t.Fatal(err)
		}
		resp, _ = post(t, ts, tok.Token, body)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("token without read-only, %s: status %d, want 403", body, resp.StatusCode)
		}
	}
}

// ---- the files ----

// The embed patterns and the directory agree: nothing on disk is left out of the binary
// unnoticed, and the disk tree passes the same format check the binary runs at start.
func TestSkillsOnDiskAreExactlyWhatIsEmbedded(t *testing.T) {
	disk := os.DirFS(skillsDir)
	var onDisk []string
	if err := fs.WalkDir(disk, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			onDisk = append(onDisk, p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var embedded []string
	for _, skill := range platformSkills {
		for _, f := range skill.Files {
			embedded = append(embedded, skill.Name+"/"+f.Path)
		}
	}
	sort.Strings(onDisk)
	sort.Strings(embedded)
	if strings.Join(onDisk, "\n") != strings.Join(embedded, "\n") {
		t.Errorf("files on disk under %s/ differ from the served catalog\non disk:\n  %s\nserved:\n  %s",
			skillsDir, strings.Join(onDisk, "\n  "), strings.Join(embedded, "\n  "))
	}
	if _, problems := loadSkills(disk); len(problems) > 0 {
		t.Errorf("the skills directory breaks the format:\n  %s", strings.Join(problems, "\n  "))
	}
	if len(platformSkills) != 3 {
		t.Errorf("%d platform skills, want 3", len(platformSkills))
	}
}

const fixtureSkillMD = "---\nname: demo\ndescription: Use when testing.\nlicense: Apache-2.0\nmetadata:\n  tools: list_alarms\n---\n\n# Demo\n\nCall `list_alarms(state: ACTIVE)`.\n"

func fixtureSkill(extra fstest.MapFS) fstest.MapFS {
	fsys := fstest.MapFS{
		"demo/SKILL.md":               {Data: []byte(fixtureSkillMD)},
		"demo/references/ground.md":   {Data: []byte("# Ground\n")},
		"demo/references/another.md":  {Data: []byte("# Another\n")},
		"other/SKILL.md":              {Data: []byte(strings.ReplaceAll(fixtureSkillMD, "demo", "other"))},
		"other/references/ground.md":  {Data: []byte("# Ground\n")},
		"other/references/another.md": {Data: []byte("# Another\n")},
	}
	for k, v := range extra {
		if v == nil {
			delete(fsys, k)
			continue
		}
		fsys[k] = v
	}
	return fsys
}

// The format check, with its controls: the well-formed fixture passes, and each thing a
// platform skill must not carry is refused by name. The no-scripts rule is the first row.
func TestSkillFormatRefusesWhatAPlatformSkillMustNotCarry(t *testing.T) {
	if skills, problems := loadSkills(fixtureSkill(nil)); len(problems) > 0 || len(skills) != 2 {
		t.Fatalf("the well-formed fixture was refused (%d skills): %v", len(skills), problems)
	}
	md := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	long := fixtureSkillMD + strings.Repeat("line\n", maxSkillMDLines)
	cases := []struct {
		name  string
		extra fstest.MapFS
		want  string
	}{
		{"a scripts directory", fstest.MapFS{"demo/scripts/run.sh": md("#!/bin/sh\n")}, "scripts/ is not allowed"},
		{"a script beside SKILL.md", fstest.MapFS{"demo/run.sh": md("#!/bin/sh\n")}, "file run.sh is not allowed"},
		{"an assets directory", fstest.MapFS{"demo/assets/logo.png": md("x\n")}, "directory assets/ is not allowed"},
		{"a non-Markdown reference", fstest.MapFS{"demo/references/data.json": md("{}\n")}, "file references/data.json is not allowed"},
		{"a nested reference directory", fstest.MapFS{"demo/references/deep/x.md": md("# x\n")}, "is not allowed"},
		{"a dot file", fstest.MapFS{"demo/references/.draft.md": md("# x\n")}, "references/.draft.md is not allowed: a name starting with . or _"},
		{"an underscore file", fstest.MapFS{"demo/references/_notes.md": md("# x\n")}, "references/_notes.md is not allowed: a name starting with . or _"},
		{"a dot directory", fstest.MapFS{"demo/.git/config.md": md("# x\n")}, ".git is not allowed: a name starting with . or _"},
		{"a file at the top level", fstest.MapFS{"README.md": md("# x\n")}, "only skill directories"},
		{"allowed-tools", fstest.MapFS{"demo/SKILL.md": md(strings.Replace(fixtureSkillMD, "license:", "allowed-tools: Bash\nlicense:", 1))}, "allowed-tools is not allowed"},
		{"an unknown frontmatter key", fstest.MapFS{"demo/SKILL.md": md(strings.Replace(fixtureSkillMD, "license:", "compatibility: x\nlicense:", 1))}, `key "compatibility" is not allowed`},
		{"a name that is not the directory", fstest.MapFS{"demo/SKILL.md": md(strings.Replace(fixtureSkillMD, "name: demo", "name: other", 1))}, "does not match its directory"},
		{"no description", fstest.MapFS{"demo/SKILL.md": md(strings.Replace(fixtureSkillMD, "description: Use when testing.\n", "", 1))}, "description is required"},
		{"another license", fstest.MapFS{"demo/SKILL.md": md(strings.Replace(fixtureSkillMD, "Apache-2.0", "MIT", 1))}, "license must be"},
		{"no tools", fstest.MapFS{"demo/SKILL.md": md(strings.Replace(fixtureSkillMD, "  tools: list_alarms\n", "  optional-tools: list_alarms\n", 1))}, "metadata.tools must name"},
		{"no frontmatter", fstest.MapFS{"demo/SKILL.md": md("# Demo\n")}, "must begin with a --- frontmatter block"},
		{"no SKILL.md", fstest.MapFS{"demo/SKILL.md": nil}, "no SKILL.md"},
		{"CRLF line endings", fstest.MapFS{"demo/references/ground.md": md("# Ground\r\n")}, "carriage return"},
		{"a control character", fstest.MapFS{"demo/references/ground.md": md("# Gro\x00und\n")}, "control character"},
		{"an ADR citation", fstest.MapFS{"demo/references/ground.md": md("Secured at the broker (ADR-025).\n")}, "cites ADR-025"},
		{"an oversized file", fstest.MapFS{"demo/references/ground.md": md(strings.Repeat("a", maxSkillFileBytes) + "\n")}, "over the 100000-byte limit"},
		{"an oversized skill", fstest.MapFS{
			"demo/references/a.md": md(strings.Repeat("a", maxSkillFileBytes-1) + "\n"),
			"demo/references/b.md": md(strings.Repeat("b", maxSkillFileBytes-1) + "\n"),
			"demo/references/c.md": md(strings.Repeat("c", maxSkillFileBytes-1) + "\n"),
			"demo/references/d.md": md(strings.Repeat("d", maxSkillFileBytes-1) + "\n"),
		}, "over the 400000-byte limit"},
		{"a long SKILL.md", fstest.MapFS{"demo/SKILL.md": md(long)}, "over the 300-line limit"},
	}
	for _, tc := range cases {
		_, problems := loadSkills(fixtureSkill(tc.extra))
		if !strings.Contains(strings.Join(problems, "\n"), tc.want) {
			t.Errorf("%s: problems %q do not report %q", tc.name, problems, tc.want)
		}
	}
}

// The digests are pinned: a change to any skill file is a deliberate diff in
// testdata/skills.sha256, reviewed beside the text it pins.
func TestSkillDigestsArePinned(t *testing.T) {
	raw, err := os.ReadFile("testdata/skills.sha256")
	if err != nil {
		t.Fatalf("reading the pinned digests: %v", err)
	}
	if problems := skillDigestProblems(platformSkills, string(raw)); len(problems) > 0 {
		t.Errorf("served skill files differ from testdata/skills.sha256:\n  %s\n\nIf the change is intended, replace the file with:\n%s",
			strings.Join(problems, "\n  "), skillDigestFile(platformSkills))
	}
	// Control: one changed byte in one file is reported.
	changed := cloneSkills(platformSkills)
	changed[0].Files[0].Body = append([]byte{}, changed[0].Files[0].Body...)
	changed[0].Files[0].Body[len(changed[0].Files[0].Body)-2] ^= 1
	sum := sha256.Sum256(changed[0].Files[0].Body)
	changed[0].Files[0].Digest = "sha256:" + hex.EncodeToString(sum[:])
	if problems := skillDigestProblems(changed, string(raw)); len(problems) != 1 {
		t.Errorf("a one-byte change produced %d digest problems, want 1: %v", len(problems), problems)
	}
}

func cloneSkills(in []Skill) []Skill {
	out := make([]Skill, len(in))
	for i, s := range in {
		out[i] = s
		out[i].Files = append([]SkillFile{}, s.Files...)
	}
	return out
}

func skillDigestFile(skills []Skill) string {
	var b strings.Builder
	for _, s := range skills {
		for _, f := range s.Files {
			fmt.Fprintf(&b, "%s  %s/%s\n", strings.TrimPrefix(f.Digest, "sha256:"), s.Name, f.Path)
		}
	}
	return b.String()
}

func skillDigestProblems(skills []Skill, pinned string) []string {
	want := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(pinned), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			want[fields[1]] = fields[0]
		}
	}
	var problems []string
	seen := map[string]bool{}
	for _, s := range skills {
		for _, f := range s.Files {
			key := s.Name + "/" + f.Path
			seen[key] = true
			switch pin, ok := want[key]; {
			case !ok:
				problems = append(problems, key+" is served but not pinned")
			case "sha256:"+pin != f.Digest:
				problems = append(problems, key+" does not match its pinned digest")
			}
		}
	}
	for key := range want {
		if !seen[key] {
			problems = append(problems, key+" is pinned but not served")
		}
	}
	sort.Strings(problems)
	return problems
}

// Files that share a name across skills are one text: each skill must be complete on its
// own, so the shared ground rules are copied, and the copies must not drift.
func TestSharedSkillFilesAreIdentical(t *testing.T) {
	byPath := map[string]SkillFile{}
	shared := 0
	for _, s := range platformSkills {
		for _, f := range s.Files {
			if f.Path == skillMDName {
				continue
			}
			if prior, ok := byPath[f.Path]; ok {
				shared++
				if prior.Digest != f.Digest {
					t.Errorf("%s differs between skills; copies of a shared reference must be identical", f.Path)
				}
			}
			byPath[f.Path] = f
		}
	}
	if shared == 0 {
		t.Fatalf("no shared reference files found; the ground rules should be in every skill")
	}
	for _, s := range platformSkills {
		found := false
		for _, f := range s.Files {
			found = found || f.Path == "references/ground-rules.md"
		}
		if !found {
			t.Errorf("%s carries no references/ground-rules.md", s.Name)
		}
	}
}

// ---- the tool-reference ratchet ----

var (
	// toolNamePattern finds anything shaped like a tool name, anywhere in a file.
	toolNamePattern = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b`)
	// toolCallPattern finds a call written as `tool(arg: value, ...)`.
	toolCallPattern = regexp.MustCompile("`([a-z][a-z0-9]*(?:_[a-z0-9]+)+)\\(([^`]*)\\)`")
	toolArgPattern  = regexp.MustCompile(`(?:^|,)\s*([A-Za-z][A-Za-z0-9]*)\s*:`)
	skillURIPattern = regexp.MustCompile(`skill://[a-z0-9-]+/[A-Za-z0-9._/-]+\.md`)
	skillRefPattern = regexp.MustCompile("`(references/[A-Za-z0-9._-]+\\.md)`")
)

// skillToolProblems reports every way a skill refers to the tool catalog wrongly:
//
//   - a tool-shaped name that is not a served tool (always-on or Preview);
//   - a tool the skill names that its metadata does not declare;
//   - a metadata.tools entry that is not always served (those are Preview: they belong
//     in optional-tools, where the skill says to stop when they are missing);
//   - an optional-tools entry that is always served, or is not a tool at all;
//   - a call written `tool(arg: ...)` naming an argument that tool does not take.
//
// served maps every tool name (with the proposal tools on) to its input arguments.
func skillToolProblems(skills []Skill, alwaysOn map[string]bool, served map[string]map[string]bool) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	for _, s := range skills {
		declared := map[string]bool{}
		for _, name := range s.Tools {
			declared[name] = true
			if !alwaysOn[name] {
				add("%s: metadata.tools names %s, which is not always served", s.Name, name)
			}
		}
		for _, name := range s.OptionalTools {
			declared[name] = true
			if _, ok := served[name]; !ok {
				add("%s: metadata.optional-tools names %s, which is not a served tool", s.Name, name)
			} else if alwaysOn[name] {
				add("%s: metadata.optional-tools names %s, which is always served; list it under tools", s.Name, name)
			}
		}
		for _, f := range s.Files {
			text := string(f.Body)
			for _, name := range toolNamePattern.FindAllString(text, -1) {
				if _, ok := served[name]; !ok {
					add("%s/%s names %s, which is not a tool this server serves", s.Name, f.Path, name)
				} else if !declared[name] {
					add("%s/%s names %s, which its metadata does not declare", s.Name, f.Path, name)
				}
			}
			for _, call := range toolCallPattern.FindAllStringSubmatch(text, -1) {
				args, ok := served[call[1]]
				if !ok {
					continue // reported above
				}
				for _, arg := range toolArgPattern.FindAllStringSubmatch(call[2], -1) {
					if !args[arg[1]] {
						add("%s/%s calls %s with %s, which that tool does not take", s.Name, f.Path, call[1], arg[1])
					}
				}
			}
		}
	}
	sort.Strings(problems)
	return problems
}

// skillLinkProblems reports a skill:// URI or a references/ path that is not a file of
// the skill that mentions it. A link to another skill's file is a dangling read for a
// host that restricts reads to the skill's own manifest.
func skillLinkProblems(skills []Skill) []string {
	var problems []string
	for _, s := range skills {
		files := map[string]bool{}
		for _, f := range s.Files {
			files[f.Path] = true
		}
		for _, f := range s.Files {
			text := string(f.Body)
			for _, uri := range skillURIPattern.FindAllString(text, -1) {
				prefix := skillURI(s.Name, "")
				if !strings.HasPrefix(uri, prefix) || !files[strings.TrimPrefix(uri, prefix)] {
					problems = append(problems, fmt.Sprintf("%s/%s links %s, which is not a file of this skill", s.Name, f.Path, uri))
				}
			}
			for _, ref := range skillRefPattern.FindAllStringSubmatch(text, -1) {
				if !files[ref[1]] {
					problems = append(problems, fmt.Sprintf("%s/%s refers to %s, which this skill does not carry", s.Name, f.Path, ref[1]))
				}
			}
		}
	}
	return problems
}

// servedToolArgs lists the tools a session is offered, with their input arguments.
func servedToolArgs(t *testing.T, cs *mcp.ClientSession) map[string]map[string]bool {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	out := map[string]map[string]bool{}
	for _, tool := range res.Tools {
		args := map[string]bool{}
		raw, _ := json.Marshal(tool.InputSchema)
		var schema struct {
			Properties map[string]any `json:"properties"`
		}
		_ = json.Unmarshal(raw, &schema)
		for name := range schema.Properties {
			args[name] = true
		}
		out[tool.Name] = args
	}
	return out
}

// The ratchet: every tool and argument a skill names is one this server serves, from
// the catalogs New's own handler offers — the always-on one, and the one a caller with
// every action scope sees when the proposal tools are switched on.
func TestSkillsNameOnlyServedToolsAndArguments(t *testing.T) {
	plain, _ := skillSession(t, "acme", coreauth.ScopeReadOnly, false)
	full, _ := skillSession(t, "acme", "read-only act:alarms act:commands location", true)
	alwaysOn := map[string]bool{}
	for name := range servedToolArgs(t, plain) {
		alwaysOn[name] = true
	}
	served := servedToolArgs(t, full)
	if len(served) <= len(alwaysOn) {
		t.Fatalf("the full catalog (%d tools) is not larger than the always-on one (%d); the Preview tools were not switched on", len(served), len(alwaysOn))
	}
	if problems := skillToolProblems(platformSkills, alwaysOn, served); len(problems) > 0 {
		t.Errorf("skills refer to the tool catalog wrongly:\n  %s", strings.Join(problems, "\n  "))
	}
	if problems := skillLinkProblems(platformSkills); len(problems) > 0 {
		t.Errorf("skills link files they do not carry:\n  %s", strings.Join(problems, "\n  "))
	}

	// Controls: each kind of wrong reference is reported.
	md := func(body string) []Skill {
		return []Skill{{Name: "demo", Tools: []string{"list_alarms"}, OptionalTools: []string{"propose_alarm_action"},
			Files: []SkillFile{{Path: skillMDName, Body: []byte(body)}}}}
	}
	controls := []struct{ body, want string }{
		{"Call `get_devise(tokens: [...])`.", "names get_devise, which is not a tool"},
		{"Call `list_alarms(stat: ACTIVE)`.", "calls list_alarms with stat"},
		{"Then call get_device.", "names get_device, which its metadata does not declare"},
		{"Read `references/missing.md`.", "refers to references/missing.md"},
		{"Read skill://other-skill/SKILL.md.", "links skill://other-skill/SKILL.md"},
	}
	for _, c := range controls {
		problems := append(skillToolProblems(md(c.body), alwaysOn, served), skillLinkProblems(md(c.body))...)
		if !strings.Contains(strings.Join(problems, "\n"), c.want) {
			t.Errorf("control %q: problems %q do not report %q", c.body, problems, c.want)
		}
	}
	preview := []Skill{{Name: "demo", Tools: []string{"propose_command"}, OptionalTools: []string{"list_alarms"},
		Files: []SkillFile{{Path: skillMDName, Body: []byte("x\n")}}}}
	problems := strings.Join(skillToolProblems(preview, alwaysOn, served), "\n")
	for _, want := range []string{"tools names propose_command, which is not always served", "optional-tools names list_alarms, which is always served"} {
		if !strings.Contains(problems, want) {
			t.Errorf("control: problems %q do not report %q", problems, want)
		}
	}
	if clean := skillToolProblems(md("Call `list_alarms(state: ACTIVE, pageSize: 100)`, then `propose_alarm_action`.\n"), alwaysOn, served); len(clean) > 0 {
		t.Errorf("control: a correct skill was refused: %v", clean)
	}
}

// The instructions point at every skill, at nothing else, and fit where a host cuts them.
func TestInstructionsPointAtEverySkill(t *testing.T) {
	if n := len(serverInstructions); n > 4096 {
		t.Errorf("instructions are %d characters; a host cuts them at 4,096", n)
	}
	if m := adrPattern.FindString(serverInstructions); m != "" {
		t.Errorf("instructions cite %s; they are served prose", m)
	}
	uris := map[string]bool{}
	for _, s := range platformSkills {
		for _, f := range s.Files {
			uris[f.URI] = true
		}
		if !strings.Contains(serverInstructions, s.SkillMD().URI) {
			t.Errorf("instructions do not point at %s", s.SkillMD().URI)
		}
	}
	for _, uri := range skillURIPattern.FindAllString(serverInstructions, -1) {
		if !uris[uri] {
			t.Errorf("instructions point at %s, which is not served", uri)
		}
	}
}

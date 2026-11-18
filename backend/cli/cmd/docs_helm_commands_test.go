// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	"helm.sh/helm/v3/pkg/strvals"
)

const docsHelmChart = "deploy/helm/devicechain"

func docsHelmInstall(words []string) bool {
	return len(words) >= 2 && words[0] == "helm" && words[1] == "install"
}

// The supported subset is deliberately literal. No shell, Helm action client,
// downloader or environment expansion is involved. Only whole environment values
// and image.tag placeholders receive explicit, reported stand-ins.
func (s *docsScan) scanHelmInstall(words []string, where, wd string, loops int) {
	invocation := strings.Join(words, " ")
	skip := func(reason string) {
		s.helmSkipped = append(s.helmSkipped, where+": "+invocation+" ("+reason+")")
	}
	if strings.Contains("/"+filepath.ToSlash(where), "/deployment/releases-and-upgrades.md:") {
		skip("excluded releases-and-upgrades page")
		return
	}
	if loops > 0 || wd == "" {
		skip("unresolved working directory or loop")
		return
	}
	var envStandIn, tagStandIn bool
	finding := func(err error) {
		s.helmFinding(where, invocation, err)
		if envStandIn {
			s.helmStandIns = append(s.helmStandIns, "checked (env stand-in): "+where+": "+invocation)
		}
		if tagStandIn {
			s.helmStandIns = append(s.helmStandIns, "checked (image tag stand-in): "+where+": "+invocation)
		}
	}
	var positional, files, sets, stringSets []string
	namespace := "default"
	for i := 2; i < len(words); i++ {
		arg := words[i]
		if !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}
		flag, value, inline := strings.Cut(arg, "=")
		switch flag {
		case "--set", "--set-string", "--namespace", "-n", "--values", "-f":
		default:
			skip("unsupported flag form: " + flag)
			return
		}
		if !inline {
			if i+1 == len(words) || strings.HasPrefix(words[i+1], "-") {
				skip("missing flag value: " + flag)
				return
			}
			i++
			value = words[i]
		}
		if flag == "--set" || flag == "--set-string" {
			var env, tag bool
			value, env, tag = docsHelmStandIns(value)
			envStandIn = envStandIn || env
			tagStandIn = tagStandIn || tag
		}
		switch flag {
		case "--set":
			sets = append(sets, value)
		case "--set-string":
			stringSets = append(stringSets, value)
		case "--namespace", "-n":
			namespace = value
		case "--values", "-f":
			files = append(files, value)
		}
	}
	// Validate after the narrowly scoped replacements, including every assignment
	// in comma-separated --set values. Partial interpolation stays unresolved.
	for _, args := range [][]string{positional, files, sets, stringSets, {namespace}} {
		for _, word := range args {
			if isPlaceholder(word) || strings.ContainsAny(word, "$`~*?<>[]{}") || strings.Contains(word, substPlaceholder) {
				skip("unresolved placeholder or dynamic argument")
				return
			}
		}
	}
	if len(positional) != 2 {
		skip("expected literal release and chart")
		return
	}
	if !literalDocsPath(positional[1]) || path.Join(wd, positional[1]) != docsHelmChart {
		skip("external or unsupported chart")
		return
	}
	values := map[string]interface{}{}
	for _, file := range files {
		if !literalDocsPath(file) {
			skip("dynamic or external values file")
			return
		}
		rel := path.Join(wd, file)
		if rel == ".." || strings.HasPrefix(rel, "../") {
			skip("values file outside repository")
			return
		}
		local := filepath.Join(s.helmRepo, filepath.FromSlash(rel))
		// Resolve symlinks too: a repo-local spelling must not read an external file.
		resolved, err := filepath.EvalSymlinks(local)
		if err != nil {
			skip("unresolved values file: " + file)
			return
		}
		repo, err := filepath.Abs(s.helmRepo)
		if err != nil {
			skip("unresolved repository")
			return
		}
		resolved, err = filepath.Abs(resolved)
		if err != nil {
			skip("unresolved values file: " + file)
			return
		}
		relative, err := filepath.Rel(repo, resolved)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			skip("values file outside repository")
			return
		}
		v, err := chartutil.ReadValuesFile(resolved)
		if err != nil {
			finding(err)
			return
		}
		values = chartutil.MergeTables(v.AsMap(), values)
	}
	// Helm merges all --set values before all --set-string values, regardless of
	// their ordering on the command line.
	for _, value := range sets {
		if err := strvals.ParseInto(value, values); err != nil {
			finding(err)
			return
		}
	}
	for _, value := range stringSets {
		if err := strvals.ParseIntoString(value, values); err != nil {
			finding(err)
			return
		}
	}
	chart, err := loader.Load(filepath.Join(s.helmRepo, filepath.FromSlash(docsHelmChart)))
	if err == nil {
		var renderValues chartutil.Values
		renderValues, err = chartutil.ToRenderValues(chart, values, chartutil.ReleaseOptions{
			Name: positional[0], Namespace: namespace, Revision: 1, IsInstall: true,
		}, chartutil.DefaultCapabilities)
		if err == nil {
			// engine.Render has no Kubernetes client; lookup cannot contact a cluster.
			_, err = engine.Render(chart, renderValues)
		}
	}
	finding(err)
}

// A deterministic base64 256-bit value satisfies rootKey without reading secrets.
const docsHelmEnvStandIn = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

var docsHelmEnvRef = regexp.MustCompile(`^(?:\$[A-Za-z_][A-Za-z0-9_]*|\$\{[A-Za-z_][A-Za-z0-9_]*\})$`)
var docsHelmTagPlaceholder = regexp.MustCompile(`^<[^<>]*>$`)

func docsHelmStandIns(value string) (string, bool, bool) {
	var assignments []string
	var env, tag bool
	start := 0
	// Helm uses unescaped commas to separate assignments. Preserve escaped commas
	// so a suffix of a partially interpolated value cannot become a whole env ref.
	for i := 0; i <= len(value); i++ {
		if i+1 < len(value) && value[i] == '\\' {
			i++
			continue
		}
		if i < len(value) && value[i] != ',' {
			continue
		}
		assignment := value[start:i]
		key, v, ok := strings.Cut(assignment, "=")
		if ok && docsHelmEnvRef.MatchString(v) {
			assignment = key + "=" + docsHelmEnvStandIn
			env = true
		} else if ok && key == "image.tag" && docsHelmTagPlaceholder.MatchString(v) {
			assignment = key + "=v0.0.0"
			tag = true
		}
		assignments = append(assignments, assignment)
		start = i + 1
	}
	return strings.Join(assignments, ","), env, tag
}

func (s *docsScan) helmFinding(where, invocation string, err error) {
	s.helmChecked++
	if err != nil {
		s.findings = append(s.findings, docsFinding{where, invocation, fmt.Sprintf("Helm render: %v", err)})
	}
}

func TestDocsHelmLiteralInstalls(t *testing.T) {
	const valid = "helm install dc deploy/helm/devicechain --set instance.id=devicechain --set-string instance.config.infrastructure.secrets.rootKey=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	for _, c := range []struct {
		name, command              string
		checked, skipped, findings int
	}{
		{"required values", valid, 1, 0, 0},
		{"tag template", valid + " --set image.tag=<version>", 1, 0, 0},
		{"variable", valid + " --set image.tag=$VERSION", 1, 0, 0},
		{"braced root key", "helm install dc deploy/helm/devicechain --set instance.id=devicechain --set-string instance.config.infrastructure.secrets.rootKey=${DC_ROOT_KEY}", 1, 0, 0},
		{"root key env", "helm install dc deploy/helm/devicechain --set instance.id=devicechain,instance.config.infrastructure.secrets.rootKey=$DC_ROOT_KEY", 1, 0, 0},
		{"inline string env", valid + " --set-string=image.tag=${VERSION}", 1, 0, 0},
		{"other placeholder", valid + " --set instance.id=<instance>", 0, 1, 0},
		{"partial env", valid + " --set image.tag=abc$VERSION", 0, 1, 0},
		{"env suffix", valid + " --set image.tag=${VERSION}abc", 0, 1, 0},
		{"env default", valid + " --set image.tag=${VERSION:-latest}", 0, 1, 0},
		{"partial tag", valid + " --set image.tag=v<version>", 0, 1, 0},
		{"env positional", "helm install $NAME deploy/helm/devicechain", 0, 1, 0},
		{"substitution", valid + " --set image.tag=$(echo version)", 0, 1, 0},
		{"external chart", "helm install dc example/devicechain", 0, 1, 0},
		{"dynamic values", valid + " -f $VALUES", 0, 1, 0},
		{"remote values", valid + " -f https://example.com/values.yaml", 0, 1, 0},
		{"missing values", valid + " -f missing.yaml", 0, 1, 0},
		{"external values", valid + " -f ../values.yaml", 0, 1, 0},
		{"unknown flag", valid + " --wait", 0, 1, 0},
		{"attached shorthand", valid + " -nexample", 0, 1, 0},
		{"missing flag value", valid + " --set", 0, 1, 0},
		{"dynamic cd", "cd $REPO; " + valid, 0, 1, 0},
		{"loop", "for x in a; do " + valid + "; done", 0, 1, 0},
		{"literal cd", "cd deploy/helm; helm install dc ./devicechain --set instance.id=devicechain --set-string instance.config.infrastructure.secrets.rootKey=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", 1, 0, 0},
		{"continuation and namespace", "helm install dc deploy/helm/devicechain \\\n --namespace=example --set instance.id=devicechain \\\n --set-string instance.config.infrastructure.secrets.rootKey=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", 1, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := scanOne(t, "```bash\n"+c.command+"\n```\n")
			if s.helmChecked != c.checked || len(s.helmSkipped) != c.skipped || len(s.findings) != c.findings || s.uncovered["helm"] != 0 {
				t.Fatalf("checked/skipped/findings = %d/%d/%d, want %d/%d/%d; findings: %s; skips: %v; uncovered: %v", s.helmChecked, len(s.helmSkipped), len(s.findings), c.checked, c.skipped, c.findings, findingTexts(s), s.helmSkipped, s.uncovered)
			}
			if c.skipped > 0 && !strings.Contains(s.helmSkipped[0], "page.md:") {
				t.Errorf("skip lost its location: %v", s.helmSkipped)
			}
		})
	}
	// Every published locale must actually render the operator examples.
	for _, page := range []string{
		"docs/docs/deployment/kubernetes-operator.md",
		"docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/kubernetes-operator.md",
		"docs/i18n/zh-CN/docusaurus-plugin-content-docs/current/deployment/kubernetes-operator.md",
	} {
		b, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(page)))
		if err != nil {
			t.Fatal(err)
		}
		s := newDocsScan()
		scanDocsText(rootCmd, page, string(b), func(string) bool { return true }, s)
		if len(s.findings) != 0 || len(s.helmSkipped) != 0 || s.helmChecked < 3 {
			t.Fatalf("%s: findings %s; checked %d; skips %v", page, findingTexts(s), s.helmChecked, s.helmSkipped)
		}
		if got := strings.Join(s.helmStandIns, "\n"); strings.Count(got, "checked (env stand-in):") != s.helmChecked || !strings.Contains(got, "checked (image tag stand-in):") {
			t.Errorf("%s: missing stand-in report: %s", page, got)
		}
	}
	for _, file := range []string{"docs/docs/deployment/releases-and-upgrades.md", "docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md", "docs/i18n/zh-CN/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md"} {
		s := newDocsScan()
		scanDocsText(rootCmd, file, "```bash\nhelm install dc deploy/helm/devicechain --set instance.id=devicechain\n```", func(string) bool { return true }, s)
		if len(s.findings) != 0 || len(s.helmSkipped) != 1 || !strings.Contains(s.helmSkipped[0], "excluded") {
			t.Errorf("excluded page %s: findings %s, skips %v", file, findingTexts(s), s.helmSkipped)
		}
	}
}

func TestDocsHelmValuesAndStrings(t *testing.T) {
	repo := t.TempDir()
	// A tiny local chart makes value types, precedence and release options observable
	// through the same renderer/verdict as the real chart, rather than mocking it.
	for name, content := range map[string]string{
		docsHelmChart + "/Chart.yaml": "apiVersion: v2\nname: fixture\nversion: 0.1.0\n",
		docsHelmChart + "/templates/check.yaml": `
{{- if not (kindIs "string" .Values.code) }}{{ fail "code must stay a string" }}{{ end }}
{{- if ne .Values.code "123" }}{{ fail "code changed" }}{{ end }}
{{- if ne .Values.nested.first "keep" }}{{ fail "values merge lost sibling" }}{{ end }}
{{- if ne .Values.nested.second "last" }}{{ fail "later file must win" }}{{ end }}
{{- if ne .Values.override "set" }}{{ fail "set must override files" }}{{ end }}
{{- if ne .Release.Name "literal" }}{{ fail "release lost" }}{{ end }}
{{- if ne .Release.Namespace "literal-ns" }}{{ fail "namespace lost" }}{{ end }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: fixture
`,
		"first.yaml":  "nested:\n  first: keep\n  second: first\noverride: file\n",
		"second.yaml": "nested:\n  second: last\n",
	} {
		file := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const base = "helm install literal deploy/helm/devicechain -f first.yaml --values=second.yaml -n literal-ns --set override=set"
	for _, c := range []struct {
		flags    string
		findings int
	}{
		{" --set-string code=123 --set code=999", 0},
		{" --set code=123", 1},
	} {
		s := newDocsScan()
		s.helmRepo = repo
		scanDocsText(rootCmd, "fixture.md", "```bash\n"+base+c.flags+"\n```", func(string) bool { return true }, s)
		if s.helmChecked != 1 || len(s.findings) != c.findings || len(s.helmSkipped) != 0 {
			t.Errorf("%s: checked %d; findings %s; skips %v", c.flags, s.helmChecked, findingTexts(s), s.helmSkipped)
		}
	}
}

// The chart supplies a default id. Helm's null override removes it, leaving a
// genuinely missing required field even though the root key can be stood in.
func TestDocsHelmEnvStandInDoesNotMaskMissingID(t *testing.T) {
	for _, flag := range []string{"--set", "--set-string"} {
		t.Run(flag, func(t *testing.T) {
			s := scanOne(t, "```bash\nhelm install dc deploy/helm/devicechain --set instance.id=null "+flag+" instance.config.infrastructure.secrets.rootKey=$DC_ROOT_KEY\n```\n")
			if s.helmChecked != 1 || len(s.helmSkipped) != 0 || len(s.findings) != 1 || !strings.Contains(findingTexts(s), "missing property 'id'") || !strings.Contains(findingTexts(s), "page.md:2") {
				t.Fatalf("missing id must produce a located render finding: %s; checked %d; skips %v", findingTexts(s), s.helmChecked, s.helmSkipped)
			}
			if len(s.helmStandIns) != 1 || !strings.Contains(s.helmStandIns[0], "checked (env stand-in): page.md:2:") {
				t.Errorf("stand-in missing from report: %v", s.helmStandIns)
			}
		})
	}
}

func TestDocsHelmStandIns(t *testing.T) {
	for _, c := range []struct {
		value, want string
		env, tag    bool
	}{
		{"key=$NAME", "key=" + docsHelmEnvStandIn, true, false},
		{"key=${_NAME1}", "key=" + docsHelmEnvStandIn, true, false},
		{"key=abc$X", "key=abc$X", false, false},
		{"key=$X/abc", "key=$X/abc", false, false},
		{"key=$X$Y", "key=$X$Y", false, false},
		{"key=${X:-default}", "key=${X:-default}", false, false},
		{"key=$1", "key=$1", false, false},
		{"key=${}", "key=${}", false, false},
		{`key=abc\,suffix=$X`, `key=abc\,suffix=$X`, false, false},
		{"image.tag=<release version>", "image.tag=v0.0.0", false, true},
		{"other=<version>", "other=<version>", false, false},
		{"image.tag=<version>-suffix", "image.tag=<version>-suffix", false, false},
		{"key=$X,image.tag=<version>", "key=" + docsHelmEnvStandIn + ",image.tag=v0.0.0", true, true},
	} {
		got, env, tag := docsHelmStandIns(c.value)
		if got != c.want || env != c.env || tag != c.tag {
			t.Errorf("%q: got %q, env=%v tag=%v; want %q, env=%v tag=%v", c.value, got, env, tag, c.want, c.env, c.tag)
		}
	}
}

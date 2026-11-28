// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"
)

// The automation area is not in the catalog until its skeleton lands, so these tests stand an area
// the full profile already deploys (mcp) in for it, through the one seam that decides membership.
func standInAreaBound(t *testing.T, area string) {
	t.Helper()
	prev := isAreaBound
	t.Cleanup(func() { isAreaBound = prev })
	isAreaBound = func(a string) bool { return a == area }
}

func mintState(t *testing.T) *State {
	t.Helper()
	st := aWritableState()
	st.Profile = "full"
	return st
}

func readMintCredential(t *testing.T, c *fake.Clientset, st *State, area string) string {
	t.Helper()
	s, err := c.CoreV1().Secrets(InstanceNamespace(st.Instance)).Get(context.Background(), areaMintSecretName(st.Instance, area), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading the mint Secret of %s: %v", area, err)
	}
	return string(s.Data[areaMintCredentialKey])
}

// Minted when absent, stamped as this instance's, never replaced by a re-run.
func TestAreaMintSecretIsMintedOnceAndNeverReplaced(t *testing.T) {
	standInAreaBound(t, "mcp")
	ctx := context.Background()
	st := mintState(t)
	c := fake.NewSimpleClientset()

	if err := ensureAreaMintSecrets(ctx, c, st); err != nil {
		t.Fatal(err)
	}
	settleStringDataLikeAnAPIServer(t, c)
	first := readMintCredential(t, c, st, "mcp")
	if len(first) < auth.MinAreaCredentialLen {
		t.Fatalf("minted credential is %d characters, want at least %d", len(first), auth.MinAreaCredentialLen)
	}
	s, _ := c.CoreV1().Secrets(InstanceNamespace(st.Instance)).Get(ctx, areaMintSecretName(st.Instance, "mcp"), metav1.GetOptions{})
	if own := readOwnership(s); !own.managed || own.owner.UID != testUID {
		t.Errorf("the mint Secret is not stamped as this instance's: %+v", own)
	}

	// bootstrap, then enable-area, then upgrade: each passes through helmInstall again.
	for i := 0; i < 3; i++ {
		if err := ensureAreaMintSecrets(ctx, c, st); err != nil {
			t.Fatalf("re-run %d: %v", i, err)
		}
		settleStringDataLikeAnAPIServer(t, c)
		if got := readMintCredential(t, c, st, "mcp"); got != first {
			t.Fatal("a re-run replaced the credential; the area and user-management would disagree until both rolled")
		}
	}

	// Only the area-bound area gets one.
	list, _ := c.CoreV1().Secrets("").List(ctx, metav1.ListOptions{})
	for _, s := range list.Items {
		if strings.Contains(s.Name, "-mint-") && s.Name != areaMintSecretName(st.Instance, "mcp") {
			t.Errorf("a mint Secret was written for a non-area-bound area: %s", s.Name)
		}
	}

	// Two instances' credentials are independent.
	other := mintState(t)
	other.Instance, other.InstanceUID = "beta", "22222222-2222-2222-2222-222222222222"
	if err := ensureAreaMintSecrets(ctx, c, other); err != nil {
		t.Fatal(err)
	}
	settleStringDataLikeAnAPIServer(t, c)
	if readMintCredential(t, c, other, "mcp") == first {
		t.Fatal("two instances were given the same credential")
	}
}

// A present Secret with no usable credential is refused, never minted over; a read error is not
// taken for absence.
func TestAreaMintSecretFailsClosed(t *testing.T) {
	standInAreaBound(t, "mcp")
	ctx := context.Background()
	st := mintState(t)
	ns := InstanceNamespace(st.Instance)
	for name, data := range map[string]map[string][]byte{
		"empty":       {},
		"too short":   {areaMintCredentialKey: []byte("short")},
		"another key": {"password": []byte(strings.Repeat("x", 64))},
	} {
		t.Run(name, func(t *testing.T) {
			c := fake.NewSimpleClientset(&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: areaMintSecretName(st.Instance, "mcp"), Namespace: ns},
				Data:       data,
			})
			if err := ensureAreaMintSecrets(ctx, c, st); err == nil {
				t.Fatal("a Secret with no usable credential was accepted")
			}
			s, _ := c.CoreV1().Secrets(ns).Get(ctx, areaMintSecretName(st.Instance, "mcp"), metav1.GetOptions{})
			if len(s.Data[areaMintCredentialKey]) != len(data[areaMintCredentialKey]) || len(s.StringData) != 0 {
				t.Fatal("the broken Secret was rewritten")
			}
		})
	}

	t.Run("a read error", func(t *testing.T) {
		c := fake.NewSimpleClientset()
		c.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(corev1.Resource("secrets"), "x", errors.New("denied"))
		})
		if err := ensureAreaMintSecrets(ctx, c, st); err == nil {
			t.Fatal("an unreadable Secret was taken for an absent one")
		}
	})

	t.Run("a failed mint writes nothing", func(t *testing.T) {
		prev := newAreaMintCredential
		t.Cleanup(func() { newAreaMintCredential = prev })
		newAreaMintCredential = func() (string, error) { return "", errors.New("no entropy") }
		c := fake.NewSimpleClientset()
		if err := ensureAreaMintSecrets(ctx, c, st); err == nil {
			t.Fatal("a failed mint was not reported")
		}
		if list, _ := c.CoreV1().Secrets("").List(ctx, metav1.ListOptions{}); len(list.Items) != 0 {
			t.Fatal("something was written")
		}
	})
}

// With the real list, no area this release can deploy is area-bound, so no run writes a mint Secret.
func TestNoMintSecretUntilAnAreaBoundAreaIsDeployed(t *testing.T) {
	ctx := context.Background()
	for _, profile := range []string{"default", "full", "telemetry", "ingest-only"} {
		st := mintState(t)
		st.Profile = profile
		c := fake.NewSimpleClientset()
		if err := ensureAreaMintSecrets(ctx, c, st); err != nil {
			t.Fatal(err)
		}
		if list, _ := c.CoreV1().Secrets("").List(ctx, metav1.ListOptions{}); len(list.Items) != 0 {
			t.Fatalf("profile %s wrote %d Secrets", profile, len(list.Items))
		}
	}
}

// chartWithAreaBound loads the embedded chart and, for the test, makes devicechain.areaBoundAreas
// name `area` instead of automation (which no profile can deploy yet). Everything else is the
// shipped chart.
func chartWithAreaBound(t *testing.T, area string) *chart.Chart {
	t.Helper()
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatal(err)
	}
	const define = "{{- define \"devicechain.areaBoundAreas\" -}}\nautomation\n{{- end -}}"
	for _, f := range ch.Templates {
		if f.Name != "templates/_helpers.tpl" {
			continue
		}
		if !strings.Contains(string(f.Data), define) {
			t.Fatal("devicechain.areaBoundAreas is not defined as this test expects; update the test with the helper")
		}
		if area != "automation" {
			f.Data = []byte(strings.Replace(string(f.Data), define, strings.Replace(define, "automation", area, 1), 1))
		}
	}
	return ch
}

// The chart's list is the Go list.
func TestTheChartsAreaBoundListIsAuthsList(t *testing.T) {
	ch := chartWithAreaBound(t, "automation")
	var helpers string
	for _, f := range ch.Templates {
		if f.Name == "templates/_helpers.tpl" {
			helpers = string(f.Data)
		}
	}
	start := strings.Index(helpers, "{{- define \"devicechain.areaBoundAreas\" -}}")
	body := helpers[start:]
	body = body[strings.Index(body, "\n")+1 : strings.Index(body, "{{- end -}}")]
	got := strings.Split(strings.TrimSpace(body), ",")
	slices.Sort(got)
	if want := auth.AreaBoundCallerNames(); !slices.Equal(got, want) {
		t.Fatalf("devicechain.areaBoundAreas = %v, auth.AreaBoundCallers = %v", got, want)
	}
}

func mintVolumes(d map[string]interface{}) (mounts, volumes []map[string]interface{}) {
	spec := d["spec"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})
	for _, v := range spec["volumes"].([]interface{}) {
		vm := v.(map[string]interface{})
		if strings.HasPrefix(vm["name"].(string), "mint-") {
			volumes = append(volumes, vm)
		}
	}
	for _, c := range spec["containers"].([]interface{}) {
		vms, _ := c.(map[string]interface{})["volumeMounts"].([]interface{})
		for _, m := range vms {
			mm := m.(map[string]interface{})
			if strings.HasPrefix(mm["name"].(string), "mint-") {
				mounts = append(mounts, mm)
			}
		}
	}
	return mounts, volumes
}

// renderChartDocs is renderDocs for a chart the test has changed.
func renderChartDocs(t *testing.T, ch *chart.Chart, vals map[string]interface{}) []map[string]interface{} {
	t.Helper()
	inst := action.NewInstall(&action.Configuration{})
	inst.ReleaseName = "dc-acme"
	inst.Namespace = "default"
	inst.DryRun = true
	inst.ClientOnly = true
	inst.APIVersions = []string{"monitoring.coreos.com/v1"}
	rel, err := inst.RunWithContext(t.Context(), ch, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	var out []map[string]interface{}
	for _, doc := range splitManifests(rel.Manifest) {
		m := map[string]interface{}{}
		if err := yaml.Unmarshal([]byte(doc), &m); err != nil || len(m) == 0 {
			continue
		}
		out = append(out, m)
	}
	return out
}

// The credential is mounted into the area-bound area and user-management, at the path both sides
// read, from the Secret dcctl writes, and into nothing else. Shown with an area the full profile
// deploys standing in for automation; with the shipped list nothing at all is mounted.
func TestTheChartMountsAreaMintCredentials(t *testing.T) {
	st := mintState(t)
	st.Profile = "full"
	st.ImageRegistry, st.ImageVersion = DefaultImageRegistry, "v0.0.0-test"
	st.Values["ingressHost"] = "localhost"
	st.Values["secretsRootKey"] = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

	check := func(t *testing.T, vals map[string]interface{}, wantSecret string) {
		seen := map[string]bool{}
		for _, d := range renderChartDocs(t, chartWithAreaBound(t, "mcp"), vals) {
			b, _ := json.Marshal(d)
			kind, _ := d["kind"].(string)
			name, _ := d["metadata"].(map[string]interface{})["name"].(string)
			refs := strings.Contains(string(b), `"`+wantSecret+`"`)
			if refs && (kind != "Deployment" || (name != "mcp" && name != "user-management")) {
				t.Errorf("%s %s references the mint Secret; only mcp and user-management may", kind, name)
			}
			if kind != "Deployment" {
				continue
			}
			mounts, volumes := mintVolumes(d)
			if name != "mcp" && name != "user-management" {
				if len(mounts)+len(volumes) != 0 {
					t.Errorf("Deployment %s mounts a mint credential", name)
				}
				continue
			}
			seen[name] = true
			if len(mounts) != 1 || len(volumes) != 1 {
				t.Fatalf("Deployment %s: %d mounts, %d volumes, want 1 and 1", name, len(mounts), len(volumes))
			}
			if mounts[0]["mountPath"] != auth.AreaMintCredentialDir+"/mcp" || mounts[0]["readOnly"] != true {
				t.Errorf("Deployment %s mounts it at %v (readOnly %v)", name, mounts[0]["mountPath"], mounts[0]["readOnly"])
			}
			sec := volumes[0]["secret"].(map[string]interface{})
			if sec["secretName"] != wantSecret {
				t.Errorf("Deployment %s reads %v, want %s", name, sec["secretName"], wantSecret)
			}
			items := sec["items"].([]interface{})
			item := items[0].(map[string]interface{})
			if len(items) != 1 || item["key"] != auth.AreaMintCredentialKey || item["path"] != auth.AreaMintCredentialKey {
				t.Errorf("Deployment %s projects %v", name, items)
			}
			// user-management starts whether or not the credential exists; the area does not.
			if want := name == "user-management"; sec["optional"] != want {
				t.Errorf("Deployment %s: optional = %v, want %v", name, sec["optional"], want)
			}
		}
		if !seen["mcp"] || !seen["user-management"] {
			t.Fatalf("the control failed: deployed %v", seen)
		}
	}

	t.Run("the Secret dcctl writes", func(t *testing.T) {
		check(t, helmValues(st), areaMintSecretName("acme", "mcp"))
	})
	t.Run("an override", func(t *testing.T) {
		vals := helmValues(st)
		inst := vals["instance"].(map[string]interface{})
		inst["areaMintSecrets"] = map[string]interface{}{"mcp": "my-own-mint"}
		check(t, vals, "my-own-mint")
	})
	t.Run("the shipped list mounts nothing on any profile", func(t *testing.T) {
		for _, profile := range []string{"default", "full"} {
			st.Profile = profile
			for _, d := range renderChartDocs(t, chartWithAreaBound(t, "automation"), helmValues(st)) {
				if d["kind"] == "PrometheusRule" {
					continue // the alert's runbook text names the Secret pattern
				}
				b, _ := json.Marshal(d)
				if strings.Contains(string(b), "-mint-") || strings.Contains(string(b), auth.AreaMintCredentialDir) {
					name, _ := d["metadata"].(map[string]interface{})["name"].(string)
					t.Errorf("profile %s: %s %s carries a mint credential", profile, d["kind"], name)
				}
			}
		}
	})
}

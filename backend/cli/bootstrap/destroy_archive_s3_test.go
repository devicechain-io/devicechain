// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

// storePod is a pod behind the in-cluster store's Service.
func storePod(name string, running, ready bool) *corev1.Pod {
	phase, status := corev1.PodPending, corev1.ConditionFalse
	if running {
		phase = corev1.PodRunning
	}
	if ready {
		status = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: infraNamespace, Labels: map[string]string{"app": "dc-object-store"}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "minio",
			Ports: []corev1.ContainerPort{{Name: "s3", ContainerPort: 9000}}}}},
		Status: corev1.PodStatus{Phase: phase, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}}},
	}
}

func storeService(target intstr.IntOrString) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "dc-object-store", Namespace: infraNamespace},
		Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "dc-object-store"},
			Ports: []corev1.ServicePort{{Port: 9000, TargetPort: target}}},
	}
}

// The pod reached is a Running, Ready one behind the Service, on the port the Service's
// port targets — by number or by name.
func TestTheStoreIsReachedThroughAReadyPodBehindItsService(t *testing.T) {
	for name, target := range map[string]intstr.IntOrString{
		"numbered target": intstr.FromInt32(9000),
		"named target":    intstr.FromString("s3"),
	} {
		t.Run(name, func(t *testing.T) {
			c := fake.NewSimpleClientset(storeService(target),
				storePod("pending", false, false), storePod("unready", true, false), storePod("ready", true, true))
			pod, port, err := readyStorePod(context.Background(), c, "dc-object-store", 9000)
			if err != nil || pod != "ready" || port != 9000 {
				t.Fatalf("reached %q:%d (%v), want ready:9000", pod, port, err)
			}
		})
	}
	c := fake.NewSimpleClientset(storeService(intstr.FromInt32(9000)), storePod("unready", true, false))
	if _, _, err := readyStorePod(context.Background(), c, "dc-object-store", 9000); err == nil {
		t.Fatal("a store with no ready pod was reached")
	}
}

// 🔴 A CREDENTIAL THAT IS NOT THIS CLUSTER'S IS NEVER PRESENTED. The store's root
// credential is what deletes; a Secret of the right name that dcctl did not write for
// this cluster is refused before anything connects.
func TestTheStoreIsNotOpenedWithAForeignCredential(t *testing.T) {
	c := fake.NewSimpleClientset(storeService(intstr.FromInt32(9000)), storePod("ready", true, true))
	clusterArchiveSecret(t, c, "another-cluster", map[string]string{"MINIO_ROOT_USER": "u", "MINIO_ROOT_PASSWORD": "p"}, nil)
	rec := aCompleteInstall()
	p := archivePlan{Action: archiveRemove, Bucket: testArchiveBucket, Prefix: testArchivePath + "/",
		Endpoint: rec.Outputs.Archive.EndpointURL, Archive: rec.Outputs.Archive, ClusterUID: testClusterUID}
	_, _, err := openArchiveStoreThroughPortForward(context.Background(), "no-such-context", c, p)
	if err == nil || !strings.Contains(err.Error(), "is not this cluster's archive credential") {
		t.Fatalf("opened with another cluster's credential: %v", err)
	}
}

// s3Double is just enough of an S3 server to answer the real client: ListObjectsV2 in
// pages of two, with or without a delimiter, and single-object DELETE. It records every
// request path so the addressing style is asserted, not assumed.
type s3Double struct {
	mu      sync.Mutex
	bucket  string
	objects map[string]int64
	failKey string
	paths   []string
	lists   int
}

func (d *s3Double) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.paths = append(d.paths, r.Method+" "+r.URL.Path)
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/"+d.bucket && r.URL.Query().Get("list-type") == "2":
		d.lists++
		d.list(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/"+d.bucket+"/"):
		key := strings.TrimPrefix(r.URL.Path, "/"+d.bucket+"/")
		if key == d.failKey {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>no</Message></Error>`)
			return
		}
		delete(d.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func (d *s3Double) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	prefix, delim := q.Get("prefix"), q.Get("delimiter")
	var entries []string // keys, or common prefixes ending in the delimiter
	seen := map[string]bool{}
	for k := range d.objects {
		rest, ok := strings.CutPrefix(k, prefix)
		if !ok {
			continue
		}
		e := k
		if delim != "" {
			if i := strings.Index(rest, delim); i >= 0 {
				e = prefix + rest[:i+1]
			}
		}
		if !seen[e] {
			seen[e] = true
			entries = append(entries, e)
		}
	}
	sort.Strings(entries)
	start := 0
	if tok := q.Get("continuation-token"); tok != "" {
		start, _ = strconv.Atoi(tok)
	}
	end := min(start+2, len(entries))
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
	fmt.Fprintf(&b, "<Name>%s</Name><Prefix>%s</Prefix><MaxKeys>2</MaxKeys><KeyCount>%d</KeyCount>", d.bucket, prefix, end-start)
	for _, e := range entries[start:end] {
		if delim != "" && strings.HasSuffix(e, delim) {
			fmt.Fprintf(&b, "<CommonPrefixes><Prefix>%s</Prefix></CommonPrefixes>", e)
		} else {
			fmt.Fprintf(&b, "<Contents><Key>%s</Key><Size>%d</Size></Contents>", e, d.objects[e])
		}
	}
	if end < len(entries) {
		fmt.Fprintf(&b, "<IsTruncated>true</IsTruncated><NextContinuationToken>%d</NextContinuationToken>", end)
	} else {
		b.WriteString("<IsTruncated>false</IsTruncated>")
	}
	b.WriteString("</ListBucketResult>")
	w.Header().Set("Content-Type", "application/xml")
	fmt.Fprint(w, b.String())
}

func newS3Double(t *testing.T) (*s3Double, *s3ArchiveStore) {
	t.Helper()
	d := &s3Double{bucket: testArchiveBucket, objects: map[string]int64{}}
	for i := 1; i <= 5; i++ {
		d.objects[fmt.Sprintf("%s/wals/%d.gz", testArchivePath, i)] = int64(i)
	}
	d.objects[testArchivePath+"-restored-20260928T000000Z/wals/1.gz"] = 7
	d.objects["dc-tsdb-acme-0badc0de/wals/1.gz"] = 9
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	return d, newS3ArchiveStore(srv.URL, "id", "secret")
}

// 🔴 EVERY PAGE, NOT THE FIRST. A listing that stopped at the first page would delete the
// first thousand objects of a real archive, pass its re-list only if that too stopped at
// one page, and leave the rest — reported as removed.
func TestTheStoreListsEveryPageAndDeletesEveryKey(t *testing.T) {
	d, store := newS3Double(t)
	ctx := context.Background()

	objs, err := store.List(ctx, testArchiveBucket, testArchivePath+"/")
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	var total int64
	for _, o := range objs {
		keys = append(keys, o.Key)
		total += o.Size
	}
	if len(keys) != 5 || total != 15 {
		t.Fatalf("listed %d keys (%d bytes) across %d pages, want 5 keys, 15 bytes: %q", len(keys), total, d.lists, keys)
	}
	if d.lists < 3 {
		t.Errorf("five keys in pages of two were read in %d request(s)", d.lists)
	}
	if err := store.Delete(ctx, testArchiveBucket, keys); err != nil {
		t.Fatal(err)
	}
	left := []string{}
	for k := range d.objects {
		left = append(left, k)
	}
	sort.Strings(left)
	if want := []string{"dc-tsdb-acme-0badc0de/wals/1.gz", testArchivePath + "-restored-20260928T000000Z/wals/1.gz"}; !slices.Equal(left, want) {
		t.Errorf("left %q, want %q", left, want)
	}
	// Path-style: the bucket is the first path segment, never a host name.
	for _, p := range d.paths {
		if !strings.Contains(p, " /"+testArchiveBucket) {
			t.Errorf("request %q is not path-style", p)
		}
	}
}

func TestTheStoreListsPrefixesAcrossPages(t *testing.T) {
	_, store := newS3Double(t)
	got, err := store.ListPrefixes(context.Background(), testArchiveBucket, "dc-tsdb-acme")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dc-tsdb-acme-0badc0de/", testArchivePath + "-restored-20260928T000000Z/", testArchivePath + "/"}
	if !slices.Equal(got, want) {
		t.Errorf("prefixes = %q, want %q", got, want)
	}
}

// A key the store refuses is an error naming it — never a silent partial delete.
func TestAKeyTheStoreRefusesIsAnError(t *testing.T) {
	d, store := newS3Double(t)
	d.failKey = testArchivePath + "/wals/3.gz"
	err := store.Delete(context.Background(), testArchiveBucket, []string{
		testArchivePath + "/wals/1.gz", testArchivePath + "/wals/3.gz", testArchivePath + "/wals/5.gz"})
	if err == nil || !strings.Contains(err.Error(), d.failKey) {
		t.Fatalf("a refused delete returned %v, want an error naming %s", err, d.failKey)
	}
}

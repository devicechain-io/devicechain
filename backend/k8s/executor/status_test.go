// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"testing"

	api "github.com/devicechain-io/dc-k8s/api/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type statusOnlyClient struct {
	client.Client
	writer client.SubResourceWriter
}

func (c statusOnlyClient) Status() client.SubResourceWriter { return c.writer }

type failingStatus struct {
	client.SubResourceWriter
	err error
	rv  string
}

func (w *failingStatus) Update(_ context.Context, obj client.Object, _ ...client.SubResourceUpdateOption) error {
	w.rv = obj.GetResourceVersion()
	return w.err
}
func TestStatusConflictRequeuesWithReadResourceVersion(t *testing.T) {
	object := &api.InstanceOperation{ObjectMeta: metav1.ObjectMeta{Name: "request", ResourceVersion: "read-version"}}
	writer := &failingStatus{err: apierrors.NewConflict(schema.GroupResource{Group: api.GroupVersion.Group, Resource: "instanceoperations"}, object.Name, fmtError("stale"))}
	result, err := updateStatus(context.Background(), statusOnlyClient{writer: writer}, object)
	if err != nil || !result.Requeue || writer.rv != "read-version" {
		t.Fatalf("conflict: %+v %v RV=%s", result, err, writer.rv)
	}
	writer.err = fmtError("denied")
	result, err = updateStatus(context.Background(), statusOnlyClient{writer: writer}, object)
	if err == nil || result.Requeue {
		t.Fatalf("non-conflict hidden: %+v %v", result, err)
	}
}

// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package apply

import (
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

const firstDocument = `apiVersion: v1
kind: ConfigMap
metadata:
  name: first
`

func TestDecodePreservesSectionCause(t *testing.T) {
	objects, err := Decode([]byte(firstDocument + "---\nmetadata:\n  name: second\n"))
	if err == nil {
		t.Fatal("expected a missing kind error in section 2")
	}
	if len(objects) != 1 || objects[0].GetName() != "first" {
		t.Fatalf("expected the first object before the error, got %v", objects)
	}
	if !strings.Contains(err.Error(), "section:[2]") || !strings.Contains(err.Error(), "Object 'Kind' is missing") || strings.Contains(err.Error(), "<nil>") {
		t.Fatalf("expected section 2 and the decoder cause, got %v", err)
	}
	cause := errors.Unwrap(err)
	if !runtime.IsMissingKind(cause) || !errors.Is(err, cause) {
		t.Fatalf("expected a wrapped missing kind error, got %v", err)
	}
}

func TestDecodeMultipleDocuments(t *testing.T) {
	objects, err := Decode([]byte(firstDocument + "---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: second\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 2 {
		t.Fatalf("expected two objects, got %d", len(objects))
	}
	if objects[0].GetKind() != "ConfigMap" || objects[0].GetName() != "first" || objects[1].GetKind() != "Secret" || objects[1].GetName() != "second" {
		t.Fatalf("unexpected decoded objects: %v", objects)
	}
}

func TestDecodeEmpty(t *testing.T) {
	objects, err := Decode(nil)
	if err != nil || len(objects) != 0 {
		t.Fatalf("expected no objects and no error, got %v, %v", objects, err)
	}
}

func TestDecodePreservesStreamCause(t *testing.T) {
	objects, err := Decode([]byte(firstDocument + "---\nmetadata: [\n"))
	if err == nil || !strings.Contains(err.Error(), "section:[2]") || len(objects) != 1 {
		t.Fatalf("expected a stream error in section 2 after one object, got %v, %v", objects, err)
	}
	if cause := errors.Unwrap(err); cause == nil || !errors.Is(err, cause) {
		t.Fatalf("expected a wrapped stream error, got %v", err)
	}
}

type conversionError struct{}

func (*conversionError) Error() string { return "conversion failed" }

func TestDecodePreservesConversionCause(t *testing.T) {
	cause := &conversionError{}
	calls := 0
	objects, err := decode([]byte(firstDocument+"---\n"+firstDocument), func(obj runtime.Object) (unstructured.Unstructured, error) {
		calls++
		if calls == 2 {
			return unstructured.Unstructured{}, cause
		}
		return ConvertSingleObjectToUnstructured(obj)
	})
	if err == nil || !strings.Contains(err.Error(), "section:[2]") || !strings.Contains(err.Error(), cause.Error()) || len(objects) != 1 {
		t.Fatalf("expected a conversion error in section 2 after one object, got %v, %v", objects, err)
	}
	var converted *conversionError
	if !errors.Is(err, cause) || !errors.As(err, &converted) || converted != cause {
		t.Fatalf("expected the original conversion cause through errors.Is/As, got %v", err)
	}
}

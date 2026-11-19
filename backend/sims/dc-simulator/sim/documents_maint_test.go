// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"testing"
	"time"
)

func TestHarborLampDocumentsValidate(t *testing.T) {
	s := harborWith(t)
	job, request := s.lampService(time.Unix(1800000017, 123456789), 0)
	schema := servedSchema(t, "device-management")
	for _, tc := range []struct {
		doc  string
		vars map[string]any
	}{
		{mutationCreateLampWindow, map[string]any{"request": request}},
		{mutationDeleteLampWindow, map[string]any{"token": job.Window}},
		{queryLampWindowByToken, map[string]any{"tokens": []string{job.Window}}},
		{queryLampWindows, map[string]any{"c": map[string]any{"pageNumber": 1, "pageSize": 100}}},
	} {
		if errs := schema.ValidateWithVariables(tc.doc, tc.vars); len(errs) > 0 {
			t.Errorf("%s: %v", tc.doc, errs)
		}
	}
}

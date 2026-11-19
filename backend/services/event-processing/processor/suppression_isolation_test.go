// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/stretchr/testify/require"
)

// This is structural: DETECT has no input through which a suppression window
// can reach it. TestSuppressionReplayPreservesAlarmFold in device-management is
// the behavioural half. Shared model/proto imports already carry rules/rosters
// and the REACT wire, so forbid suppression symbols rather than those packages.
func TestSuppressionCannotReachDetectOrReact(t *testing.T) {
	require.NoError(t, filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Ident:
				requireNoSuppressionSymbol(t, x.Name, path)
			case *ast.BasicLit:
				if x.Kind == token.STRING {
					requireNoSuppressionSymbol(t, x.Value, path)
				}
			}
			return true
		})
		return nil
	}))
	// RaiseAlarmRequest is the actual JSON wire marshalled by alarmClient.Dispatch.
	for _, typ := range []reflect.Type{reflect.TypeOf(dmmodel.RaiseAlarmRequest{}), reflect.TypeOf(react.AlarmRequest{})} {
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			require.NotContains(t, strings.ToLower(f.Name), "suppress", typ.String())
			require.NotContains(t, strings.ToLower(f.Tag.Get("json")), "suppress", typ.String())
		}
	}
}

// The alarm-suppression vocabulary only: an unrelated "suppress" (a duplicate, a log line)
// is not a window and must not trip this guard.
var suppressionSymbols = []string{"alarmsuppression", "suppressionwindow", "suppresseduntil", "raisedduringsuppression", "suppressionstatus", "suppressionfor"}

func requireNoSuppressionSymbol(t *testing.T, text, where string) {
	t.Helper()
	lower := strings.ToLower(text)
	for _, symbol := range suppressionSymbols {
		require.NotContains(t, lower, symbol, where)
	}
}

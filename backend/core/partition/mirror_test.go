// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package partition

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type mapBucket struct {
	m      map[string][]byte
	putErr error
	getErr error
	puts   int
}

func newMapBucket() *mapBucket { return &mapBucket{m: map[string][]byte{}} }

func (b *mapBucket) Put(k string, v []byte) error {
	if b.putErr != nil {
		return b.putErr
	}
	b.puts++
	b.m[k] = append([]byte(nil), v...)
	return nil
}

func (b *mapBucket) Get(k string) ([]byte, bool, error) {
	if b.getErr != nil {
		return nil, false, b.getErr
	}
	v, ok := b.m[k]
	return v, ok, nil
}

func sampleTable() Table {
	t := NewTable(DefaultP)
	t.Groups[5] = 1
	t.Groups[6] = 2
	t.Overrides = map[string]int{"big-tenant": 6, "other": 5}
	t.Generation = 7
	return t
}

func TestTheMirrorRoundTripsTheTable(t *testing.T) {
	b := newMapBucket()
	want := sampleTable()
	require.NoError(t, Publish(b, want))
	got, err := Load(b)
	require.NoError(t, err)
	require.True(t, want.Equal(got))
	require.NoError(t, Verify(b, want))
}

func TestAnEmptyMirrorIsNoTableNotAnEmptyTable(t *testing.T) {
	_, err := Load(newMapBucket())
	require.True(t, errors.Is(err, ErrNoTable))
	require.True(t, errors.Is(Verify(newMapBucket(), sampleTable()), ErrNoTable))
}

// Verify is the consistency check: a mirror that differs from the table in any field is
// reported, not forgiven.
func TestVerifyNamesAMirrorThatDiffersFromTheTable(t *testing.T) {
	b := newMapBucket()
	require.NoError(t, Publish(b, sampleTable()))

	mutations := map[string]func(*Table){
		"a group moved":     func(x *Table) { x.Groups[5] = 2 },
		"an override moved": func(x *Table) { x.Overrides["other"] = 4 },
		"an override added": func(x *Table) { x.Overrides["new"] = 1 },
		"an override gone":  func(x *Table) { delete(x.Overrides, "other") },
		"generation behind": func(x *Table) { x.Generation = 6 },
		"generation ahead":  func(x *Table) { x.Generation = 8 },
	}
	for name, mutate := range mutations {
		x := sampleTable()
		mutate(&x)
		require.Error(t, Verify(b, x), name)
	}
}

func TestPublishIsByteStable(t *testing.T) {
	a, err := Encode(sampleTable())
	require.NoError(t, err)
	c, err := Encode(sampleTable())
	require.NoError(t, err)
	require.Equal(t, a, c)
}

// 🔑 P IMMUTABILITY, at the mirror: a table with another P never replaces the mirrored one.
func TestPublishRefusesToChangeP(t *testing.T) {
	b := newMapBucket()
	require.NoError(t, Publish(b, sampleTable()))
	before := string(b.m[TableKey])

	err := Publish(b, NewTable(DefaultP*2))
	require.True(t, errors.Is(err, ErrPChanged), "%v", err)
	require.Equal(t, before, string(b.m[TableKey]), "the refused write must not land")
}

func TestPublishRefusesAnInvalidTableAndWritesNothing(t *testing.T) {
	b := newMapBucket()
	bad := NewTable(DefaultP)
	bad.Groups[0] = -1
	require.Error(t, Publish(b, bad))
	require.Equal(t, 0, b.puts)
}

func TestPublishSurfacesBrokerFailures(t *testing.T) {
	b := newMapBucket()
	b.putErr = errors.New("bucket full")
	require.ErrorContains(t, Publish(b, sampleTable()), "bucket full")

	b = newMapBucket()
	b.getErr = errors.New("broker down")
	err := Publish(b, sampleTable())
	require.ErrorContains(t, err, "broker down")
	require.Equal(t, 0, b.puts, "an unreadable mirror must not be overwritten blind")
}

func TestPublishRewritesAnUnchangedTableToKeepItAlive(t *testing.T) {
	b := newMapBucket()
	require.NoError(t, Publish(b, sampleTable()))
	require.NoError(t, Publish(b, sampleTable()))
	require.Equal(t, 2, b.puts)
}

func TestDecodeRefusesWhatItWouldHalfUnderstand(t *testing.T) {
	valid, err := Encode(NewTable(DefaultP))
	require.NoError(t, err)
	withExtra := append(append([]byte(nil), valid[:len(valid)-1]...), []byte(`,"future":1}`)...)
	_, err = Decode(withExtra)
	require.Error(t, err, "a table with a field this reader does not know must not decode")
	_, err = Decode(valid)
	require.NoError(t, err)
	_, err = Decode([]byte(`not json`))
	require.Error(t, err)
	raw, err := Encode(NewTable(DefaultP))
	require.NoError(t, err)
	_, err = Decode(raw)
	require.NoError(t, err)
	_, err = Decode([]byte(`{"p":64,"groups":[0]}`))
	require.Error(t, err, "a short group list must not decode into a routable table")
}

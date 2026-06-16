// Copyright 2018-2020 Red Hat, Intel Corp.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cniovs

import (
	"testing"

	"github.com/ovn-org/libovsdb/ovsdb"
	"github.com/stretchr/testify/assert"
)

// These exercise the pure decoding of libovsdb's set/optional-column
// representation, which is the subtle part of ovsdb.go. They need no live
// ovsdb-server: the helpers are split out from the select-then-decode
// functions exactly so they can be tested against hand-built rows/values.

func TestPortsSetNonEmpty(t *testing.T) {
	testCases := []struct {
		name string
		row  ovsdb.Row
		want bool
	}{
		{
			name: "missing ports column",
			row:  ovsdb.Row{},
			want: false,
		},
		{
			name: "empty set",
			row:  ovsdb.Row{"ports": ovsdb.OvsSet{GoSet: []interface{}{}}},
			want: false,
		},
		{
			name: "multi-element set",
			row: ovsdb.Row{"ports": ovsdb.OvsSet{GoSet: []interface{}{
				ovsdb.UUID{GoUUID: "11111111-1111-1111-1111-111111111111"},
				ovsdb.UUID{GoUUID: "22222222-2222-2222-2222-222222222222"},
			}}},
			want: true,
		},
		{
			name: "single element collapsed to bare UUID",
			row:  ovsdb.Row{"ports": ovsdb.UUID{GoUUID: "33333333-3333-3333-3333-333333333333"}},
			want: true,
		},
		{
			name: "unexpected type",
			row:  ovsdb.Row{"ports": "not-a-set"},
			want: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, portsSetNonEmpty(tc.row))
		})
	}
}

func TestMacFromValue(t *testing.T) {
	testCases := []struct {
		name string
		val  interface{}
		want string
	}{
		{
			name: "bare string",
			val:  "fe:ed:de:ad:be:ef",
			want: "fe:ed:de:ad:be:ef",
		},
		{
			name: "empty optional set",
			val:  ovsdb.OvsSet{GoSet: []interface{}{}},
			want: "",
		},
		{
			name: "single-element string set",
			val:  ovsdb.OvsSet{GoSet: []interface{}{"ca:fe:ca:fe:ca:fe"}},
			want: "ca:fe:ca:fe:ca:fe",
		},
		{
			name: "set with non-string element",
			val:  ovsdb.OvsSet{GoSet: []interface{}{42}},
			want: "",
		},
		{
			name: "unexpected type",
			val:  123,
			want: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, macFromValue(tc.val))
		})
	}
}

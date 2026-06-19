/*
 * Copyright(c) 2026 The userspace-cni Authors.
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package daemon

import (
	"errors"
	"reflect"
	"testing"
)

func sockets(ms []Memif) []string {
	if len(ms) == 0 {
		return nil
	}
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Socket)
	}
	return out
}

func TestDiff(t *testing.T) {
	A := Memif{Socket: "/run/vpp/app1/a.sock", BridgeID: 100, Mode: "ethernet"}
	B := Memif{Socket: "/run/vpp/app2/b.sock", BridgeID: 100, Mode: "ethernet"}
	Bact := Memif{Socket: "/run/vpp/app2/b.sock", SwIfIndex: 9}
	Cact := Memif{Socket: "/run/vpp/old/c.sock", SwIfIndex: 10}

	tests := []struct {
		name            string
		desired, actual []Memif
		wantCreate      []string
		wantDelete      []string
	}{
		{"restore all (post VPP restart)", []Memif{A, B}, nil, []string{A.Socket, B.Socket}, nil},
		{"gc all orphans", nil, []Memif{Bact, Cact}, nil, []string{Bact.Socket, Cact.Socket}},
		{"no-op when matched", []Memif{B}, []Memif{Bact}, nil, nil},
		{"mixed: create A, gc C", []Memif{A, B}, []Memif{Bact, Cact}, []string{A.Socket}, []string{Cact.Socket}},
		{"empty/empty", nil, nil, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			toCreate, toDelete := Diff(tc.desired, tc.actual)
			if got := sockets(toCreate); !reflect.DeepEqual(got, tc.wantCreate) {
				t.Errorf("toCreate = %v, want %v", got, tc.wantCreate)
			}
			if got := sockets(toDelete); !reflect.DeepEqual(got, tc.wantDelete) {
				t.Errorf("toDelete = %v, want %v", got, tc.wantDelete)
			}
		})
	}
}

type fakeDataplane struct {
	dump      []Memif
	dumpErr   error
	created   []Memif
	deleted   []uint32
	createErr error
}

func (f *fakeDataplane) DumpMasters() ([]Memif, error) { return f.dump, f.dumpErr }
func (f *fakeDataplane) CreateMaster(m Memif) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.created = append(f.created, m)
	return nil
}
func (f *fakeDataplane) DeleteMaster(m Memif) error {
	f.deleted = append(f.deleted, m.SwIfIndex)
	return nil
}
func (f *fakeDataplane) SweepOrphanSockets(map[string]struct{}) (int, error) { return 0, nil }

func TestReconcile(t *testing.T) {
	A := Memif{Socket: "/run/vpp/app1/a.sock", BridgeID: 100, Mode: "ethernet"}
	Bact := Memif{Socket: "/run/vpp/app2/b.sock", SwIfIndex: 9}
	Cact := Memif{Socket: "/run/vpp/old/c.sock", SwIfIndex: 10}

	// Desired {A,B}, VPP has {B,C}: create A, delete C (swIfIndex 10), keep B.
	f := &fakeDataplane{dump: []Memif{Bact, Cact}}
	created, deleted, err := Reconcile(f, []Memif{A, {Socket: "/run/vpp/app2/b.sock"}}, nil)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if created != 1 || deleted != 1 {
		t.Fatalf("created=%d deleted=%d, want 1/1", created, deleted)
	}
	if len(f.created) != 1 || f.created[0].Socket != A.Socket {
		t.Errorf("created = %+v, want [A]", f.created)
	}
	if !reflect.DeepEqual(f.deleted, []uint32{10}) {
		t.Errorf("deleted = %v, want [10]", f.deleted)
	}
}

func TestReconcileIdempotent(t *testing.T) {
	Bact := Memif{Socket: "/run/vpp/app2/b.sock", SwIfIndex: 9}
	f := &fakeDataplane{dump: []Memif{Bact}}
	created, deleted, err := Reconcile(f, []Memif{{Socket: "/run/vpp/app2/b.sock"}}, nil)
	if err != nil || created != 0 || deleted != 0 {
		t.Fatalf("steady state should be a no-op: created=%d deleted=%d err=%v", created, deleted, err)
	}
}

func TestReconcileDumpError(t *testing.T) {
	f := &fakeDataplane{dumpErr: errors.New("vpp down")}
	if _, _, err := Reconcile(f, []Memif{{Socket: "/run/vpp/app1/a.sock"}}, nil); err == nil {
		t.Fatal("expected error when dump fails")
	}
	if len(f.created) != 0 || len(f.deleted) != 0 {
		t.Error("must not mutate VPP when the initial dump fails")
	}
}

func TestReconcileGCGuardKeepsUnconfirmed(t *testing.T) {
	// Two orphans; the guard confirms only the one whose socket is "gone".
	gone := Memif{Socket: "/run/vpp/old/gone.sock", SwIfIndex: 1}
	present := Memif{Socket: "/run/vpp/app1/present.sock", SwIfIndex: 2}
	f := &fakeDataplane{dump: []Memif{gone, present}}
	guard := func(m Memif) bool { return m.Socket == gone.Socket }

	created, deleted, err := Reconcile(f, nil, guard)
	if err != nil {
		t.Fatal(err)
	}
	if created != 0 || deleted != 1 {
		t.Fatalf("created=%d deleted=%d, want 0/1", created, deleted)
	}
	if !reflect.DeepEqual(f.deleted, []uint32{1}) {
		t.Errorf("deleted = %v, want [1] (only the confirmed-abandoned orphan)", f.deleted)
	}
}

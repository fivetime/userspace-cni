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

// Package daemon implements the per-node restore daemon: it watches the node
// VPP connection and, on (re)connect, re-asserts the memif masters userspace-cni
// owns — VPP's runtime state is non-persistent, so a VPP restart drops them and
// no pod-lifecycle event re-triggers the CNI. See
// docs/proposals/vpp-memif-restore-daemon.md.
//
// This file is the dataplane-agnostic reconcile core (pure, fully unit-tested).
// The govpp-backed Dataplane and the connection watcher live alongside it.
package daemon

// Memif is a userspace-cni memif master that should (desired) or does (actual)
// exist in the node VPP. Socket is the stable per-pod identity — one master per
// socket — so reconciliation diffs by Socket.
type Memif struct {
	// Socket is the host path the master listens on (the .sock file in the
	// userspace-cni shared dir). Identity key for diffing.
	Socket string
	// ID is the memif interface id; must match the pod's slave (default 0).
	ID uint32
	// BridgeID is the L2 bridge-domain to join; 0 means "not bridged".
	BridgeID uint32
	// Mode is the memif mode: "ethernet" or "ip".
	Mode string
	// SwIfIndex is the VPP-assigned index; set on actual entries (used to delete).
	SwIfIndex uint32
}

// Dataplane is the seam over the node VPP. The govpp-backed implementation is
// provided separately; tests use a fake. All methods must be idempotent.
type Dataplane interface {
	// DumpMasters returns the userspace-cni memif masters currently in VPP.
	// It must not return interfaces other components own (non-memif, or memifs
	// whose socket is outside the userspace-cni shared dir).
	DumpMasters() ([]Memif, error)
	// CreateMaster (re)creates the memif master on m.Socket and bridges it into
	// m.BridgeID when non-zero. The pod's slave auto-reconnects.
	CreateMaster(m Memif) error
	// DeleteMaster removes the memif at swIfIndex.
	DeleteMaster(swIfIndex uint32) error
}

// Diff compares the desired memif masters against what VPP actually has,
// returning the masters to create (desired-but-absent — the restore case after a
// VPP restart) and to delete (present-but-not-desired — orphans, e.g. a CNI DEL
// that never ran). Identity is the socket path. Diff is pure and order-preserving.
func Diff(desired, actual []Memif) (toCreate, toDelete []Memif) {
	have := make(map[string]struct{}, len(actual))
	for _, a := range actual {
		have[a.Socket] = struct{}{}
	}
	want := make(map[string]struct{}, len(desired))
	for _, d := range desired {
		want[d.Socket] = struct{}{}
	}
	for _, d := range desired {
		if _, ok := have[d.Socket]; !ok {
			toCreate = append(toCreate, d)
		}
	}
	for _, a := range actual {
		if _, ok := want[a.Socket]; !ok {
			toDelete = append(toDelete, a)
		}
	}
	return toCreate, toDelete
}

// Reconcile converges the node VPP to the desired set: dump, diff, create the
// missing masters and delete the orphans. Idempotent — a no-op when VPP already
// matches. Returns counts; stops at the first dataplane error (the next
// reconcile retries, the operations being individually idempotent).
//
// gcOrphan, if non-nil, gates each orphan deletion: the orphan is deleted only
// when gcOrphan(m) returns true. This confines GC to memifs CNI has truly
// abandoned and avoids racing an in-flight CNI ADD (see SocketGone). nil deletes
// every orphan.
func Reconcile(dp Dataplane, desired []Memif, gcOrphan func(Memif) bool) (created, deleted int, err error) {
	actual, err := dp.DumpMasters()
	if err != nil {
		return 0, 0, err
	}
	toCreate, toDelete := Diff(desired, actual)
	for _, m := range toCreate {
		if err = dp.CreateMaster(m); err != nil {
			return created, deleted, err
		}
		created++
	}
	for _, m := range toDelete {
		if gcOrphan != nil && !gcOrphan(m) {
			continue // not confirmed abandoned (e.g. CNI ADD in flight) — keep it
		}
		if err = dp.DeleteMaster(m.SwIfIndex); err != nil {
			return created, deleted, err
		}
		deleted++
	}
	return created, deleted, nil
}

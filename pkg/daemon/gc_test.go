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
	"os"
	"path/filepath"
	"testing"
)

func TestShouldGC(t *testing.T) {
	r := &Reconciler{Grace: 2}

	// A vanished socket is GC'd immediately.
	if !r.shouldGC(Memif{Socket: "/no/such/sock"}) {
		t.Fatal("absent socket should GC immediately")
	}

	// A present socket (could be an in-flight ADD) is kept until it has stayed an
	// orphan for Grace consecutive reconciles.
	sock := filepath.Join(t.TempDir(), "a.sock")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	m := Memif{Socket: sock}

	r.round = 1
	if r.shouldGC(m) {
		t.Fatal("round 1: present-socket orphan should be kept")
	}
	r.round = 2
	if !r.shouldGC(m) {
		t.Fatal("round 2: present-socket orphan past grace should GC")
	}

	// A non-consecutive round resets the streak (re-seen as a fresh orphan).
	r.round = 5
	if r.shouldGC(m) {
		t.Fatal("round 5 after a gap: streak should reset, keep")
	}
}

func TestShouldGCGraceZeroKeepsPresentSocket(t *testing.T) {
	// Grace 0 = conservative: never GC a present socket (only a gone one).
	r := &Reconciler{Grace: 0}
	sock := filepath.Join(t.TempDir(), "b.sock")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r.round = 1
	if r.shouldGC(Memif{Socket: sock}) {
		t.Fatal("Grace 0 must never GC a present socket")
	}
}

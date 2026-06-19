//go:build linux

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
	"time"
)

func TestSweepOrphanSockets(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "memif-aaaaaaaaaaaa-net1.sock")   // kept: in keep
	orphan := filepath.Join(dir, "memif-bbbbbbbbbbbb-net1.sock") // removed: not kept, old
	recent := filepath.Join(dir, "memif-cccccccccccc-net1.sock") // kept: not kept but fresh
	other := filepath.Join(dir, "api.sock")                      // kept: not a memif socket
	for _, f := range []string{live, orphan, recent, other} {
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * orphanSocketMinAge)
	for _, f := range []string{live, orphan, other} {
		if err := os.Chtimes(f, old, old); err != nil {
			t.Fatal(err)
		}
	}

	dp := &vppDataplane{}
	removed, err := dp.SweepOrphanSockets(map[string]struct{}{live: {}})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed=%d, want 1 (only the aged orphan)", removed)
	}
	exists := func(p string) bool { _, e := os.Stat(p); return e == nil }
	if !exists(live) {
		t.Error("live socket (in keep) must be kept")
	}
	if exists(orphan) {
		t.Error("aged orphan must be removed")
	}
	if !exists(recent) {
		t.Error("recent orphan must be kept (in-flight ADD guard)")
	}
	if !exists(other) {
		t.Error("non-memif socket must be left alone")
	}
}

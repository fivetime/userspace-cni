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
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
)

// Status tracks the restore daemon's liveness/observability state and serves it
// over HTTP: /healthz (process up), /readyz (VPP connected + last reconcile ok),
// /metrics (Prometheus text, no external client dependency).
type Status struct {
	connected       atomic.Bool
	reconcileTotal  atomic.Int64
	reconcileErrors atomic.Int64
	memifsCreated   atomic.Int64
	memifsDeleted   atomic.Int64

	mu              sync.Mutex
	lastReconcileOK bool
	sawReconcile    bool
}

// SetConnected records the VPP API connection state.
func (s *Status) SetConnected(v bool) { s.connected.Store(v) }

// RecordReconcile updates counters and the last-reconcile result.
func (s *Status) RecordReconcile(created, deleted int, err error) {
	s.reconcileTotal.Add(1)
	if err != nil {
		s.reconcileErrors.Add(1)
	}
	s.memifsCreated.Add(int64(created))
	s.memifsDeleted.Add(int64(deleted))
	s.mu.Lock()
	s.lastReconcileOK = err == nil
	s.sawReconcile = true
	s.mu.Unlock()
}

func (s *Status) ready() bool {
	if !s.connected.Load() {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Ready once connected and the first reconcile has succeeded.
	return !s.sawReconcile || s.lastReconcileOK
}

// Handler returns the /healthz, /readyz and /metrics endpoints.
func (s *Status) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if s.ready() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ready\n"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready: VPP disconnected or last reconcile failed\n"))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		var connected int
		if s.connected.Load() {
			connected = 1
		}
		fmt.Fprint(w,
			"# HELP userspace_cni_restore_vpp_connected VPP API connection state (1=connected).\n"+
				"# TYPE userspace_cni_restore_vpp_connected gauge\n")
		fmt.Fprintf(w, "userspace_cni_restore_vpp_connected %d\n", connected)
		fmt.Fprint(w,
			"# HELP userspace_cni_restore_reconcile_total Reconciles run.\n"+
				"# TYPE userspace_cni_restore_reconcile_total counter\n")
		fmt.Fprintf(w, "userspace_cni_restore_reconcile_total %d\n", s.reconcileTotal.Load())
		fmt.Fprint(w,
			"# HELP userspace_cni_restore_reconcile_errors_total Reconciles that returned an error.\n"+
				"# TYPE userspace_cni_restore_reconcile_errors_total counter\n")
		fmt.Fprintf(w, "userspace_cni_restore_reconcile_errors_total %d\n", s.reconcileErrors.Load())
		fmt.Fprint(w,
			"# HELP userspace_cni_restore_memifs_created_total memif masters (re)created.\n"+
				"# TYPE userspace_cni_restore_memifs_created_total counter\n")
		fmt.Fprintf(w, "userspace_cni_restore_memifs_created_total %d\n", s.memifsCreated.Load())
		fmt.Fprint(w,
			"# HELP userspace_cni_restore_memifs_deleted_total orphan memif masters GC'd.\n"+
				"# TYPE userspace_cni_restore_memifs_deleted_total counter\n")
		fmt.Fprintf(w, "userspace_cni_restore_memifs_deleted_total %d\n", s.memifsDeleted.Load())
	})
	return mux
}

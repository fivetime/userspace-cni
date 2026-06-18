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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}

func TestStatusHealthReadyMetrics(t *testing.T) {
	s := &Status{}
	h := s.Handler()

	// healthz is always OK once the process is up.
	if rr := get(h, "/healthz"); rr.Code != http.StatusOK {
		t.Errorf("healthz = %d, want 200", rr.Code)
	}
	// Disconnected → not ready.
	if rr := get(h, "/readyz"); rr.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz (disconnected) = %d, want 503", rr.Code)
	}
	// Connected + a successful reconcile → ready.
	s.SetConnected(true)
	s.RecordReconcile(2, 1, nil)
	if rr := get(h, "/readyz"); rr.Code != http.StatusOK {
		t.Errorf("readyz (connected, ok) = %d, want 200", rr.Code)
	}
	// A failed reconcile → not ready again.
	s.RecordReconcile(0, 0, errors.New("boom"))
	if rr := get(h, "/readyz"); rr.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz (reconcile failed) = %d, want 503", rr.Code)
	}

	body := get(h, "/metrics").Body.String()
	for _, want := range []string{
		"userspace_cni_restore_vpp_connected 1",
		"userspace_cni_restore_reconcile_total 2",
		"userspace_cni_restore_reconcile_errors_total 1",
		"userspace_cni_restore_memifs_created_total 2",
		"userspace_cni_restore_memifs_deleted_total 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q in:\n%s", want, body)
		}
	}
}

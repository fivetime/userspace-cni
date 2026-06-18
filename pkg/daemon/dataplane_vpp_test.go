//go:build linux && vppmock

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

// Exercises the govpp-backed Dataplane against govpp's mock adapter (no real
// VPP). Gated behind the `vppmock` tag so the default unit-test run stays
// host-portable; run with:  go test -tags vppmock ./pkg/daemon/
package daemon

import (
	"testing"

	"go.fd.io/govpp/adapter/mock"
	"go.fd.io/govpp/binapi/memclnt"
	"go.fd.io/govpp/binapi/memif"
	"go.fd.io/govpp/core"
)

func TestVPPDumpMastersFilters(t *testing.T) {
	mockVpp := mock.NewVppAdapter()
	conn, err := core.Connect(mockVpp)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Disconnect()
	ch, err := conn.NewAPIChannel()
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()

	mockVpp.MockReplyWithContext(
		// MemifSocketFilenameDump (1st multi-request, seq 1): socket 1 under the
		// prefix, socket 2 outside it.
		mock.MsgWithContext{Msg: &memif.MemifSocketFilenameDetails{SocketID: 1, SocketFilename: "/run/vpp/app1/a.sock"}, Multipart: true, SeqNum: 1},
		mock.MsgWithContext{Msg: &memif.MemifSocketFilenameDetails{SocketID: 2, SocketFilename: "/elsewhere/b.sock"}, Multipart: true, SeqNum: 1},
		mock.MsgWithContext{Msg: &memclnt.ControlPingReply{}, Multipart: true, SeqNum: 1},
		// MemifDump (2nd multi-request, seq 2): a master in-prefix (kept), a master
		// out-of-prefix (dropped), and a slave in-prefix (dropped).
		mock.MsgWithContext{Msg: &memif.MemifDetails{SwIfIndex: 5, SocketID: 1, Role: memif.MEMIF_ROLE_API_MASTER, Mode: memif.MEMIF_MODE_API_ETHERNET}, Multipart: true, SeqNum: 2},
		mock.MsgWithContext{Msg: &memif.MemifDetails{SwIfIndex: 6, SocketID: 2, Role: memif.MEMIF_ROLE_API_MASTER}, Multipart: true, SeqNum: 2},
		mock.MsgWithContext{Msg: &memif.MemifDetails{SwIfIndex: 7, SocketID: 1, Role: memif.MEMIF_ROLE_API_SLAVE}, Multipart: true, SeqNum: 2},
		mock.MsgWithContext{Msg: &memclnt.ControlPingReply{}, Multipart: true, SeqNum: 2},
	)

	dp := &vppDataplane{ch: ch, socketPrefixes: []string{"/run/vpp"}}
	got, err := dp.DumpMasters()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("DumpMasters returned %d memifs, want 1 (master in-prefix only): %+v", len(got), got)
	}
	if got[0].SwIfIndex != 5 || got[0].Socket != "/run/vpp/app1/a.sock" || got[0].Mode != "ethernet" {
		t.Errorf("unexpected memif: %+v", got[0])
	}
}

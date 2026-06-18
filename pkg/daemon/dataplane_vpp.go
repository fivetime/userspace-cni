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
	"fmt"
	"strings"

	govppapi "go.fd.io/govpp/api"
	"go.fd.io/govpp/binapi/interface_types"
	"go.fd.io/govpp/binapi/memif"

	vppbridge "github.com/intel/userspace-cni-network-plugin/cnivpp/api/bridge"
	vppinterface "github.com/intel/userspace-cni-network-plugin/cnivpp/api/interface"
	vppmemif "github.com/intel/userspace-cni-network-plugin/cnivpp/api/memif"
)

// vppDataplane implements Dataplane over a govpp API channel. It reuses
// userspace-cni's proven cnivpp/api wrappers for create/delete and dumps memifs
// via the binary API (cnivpp/api/memif has no exported list). socketPrefix scopes
// reconciliation to the userspace-cni shared dir so it never touches a memif (or
// any interface) owned by something else.
type vppDataplane struct {
	ch             govppapi.Channel
	socketPrefixes []string
}

// NewVPPDataplane wraps an existing govpp API channel. socketPrefix is a
// comma-separated list of shared-dir roots (e.g. "/run/vpp,/var/run/vpp"); only
// memif masters whose socket is under one of them are managed, confining the
// daemon to memifs userspace-cni created. "" disables the guard (memif_dump
// already excludes non-memifs).
func NewVPPDataplane(ch govppapi.Channel, socketPrefix string) Dataplane {
	var prefixes []string
	for _, p := range strings.Split(socketPrefix, ",") {
		if p = strings.TrimSpace(p); p != "" {
			prefixes = append(prefixes, p)
		}
	}
	return &vppDataplane{ch: ch, socketPrefixes: prefixes}
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func (d *vppDataplane) DumpMasters() ([]Memif, error) {
	socks, err := d.dumpSockets()
	if err != nil {
		return nil, err
	}
	var out []Memif
	req := d.ch.SendMultiRequest(&memif.MemifDump{})
	for {
		det := &memif.MemifDetails{}
		stop, rerr := req.ReceiveReply(det)
		if rerr != nil {
			return nil, fmt.Errorf("memif dump: %w", rerr)
		}
		if stop {
			break
		}
		if det.Role != memif.MEMIF_ROLE_API_MASTER {
			continue // we only own the host-side masters
		}
		sock := socks[det.SocketID]
		if len(d.socketPrefixes) > 0 && !hasAnyPrefix(sock, d.socketPrefixes) {
			continue
		}
		out = append(out, Memif{
			Socket:    sock,
			ID:        det.ID,
			Mode:      modeString(det.Mode),
			SwIfIndex: uint32(det.SwIfIndex),
		})
	}
	return out, nil
}

func (d *vppDataplane) CreateMaster(m Memif) error {
	// Reuse cnivpp's socket find-or-create (allocates a socket id) + memif create.
	socketID, err := vppmemif.CreateMemifSocket(d.ch, m.Socket)
	if err != nil {
		return fmt.Errorf("memif socket %s: %w", m.Socket, err)
	}
	swIfIndex, err := vppmemif.CreateMemifInterface(d.ch, vppmemif.CreateParams{
		SocketID:   socketID,
		Role:       memif.MEMIF_ROLE_API_MASTER,
		Mode:       parseMode(m.Mode),
		RxQueues:   m.RxQueues,   // 0 → CreateMemifInterface default (1)
		TxQueues:   m.TxQueues,   // 0 → default (1)
		RingSize:   m.RingSize,   // 0 → default (1024)
		BufferSize: m.BufferSize, // 0 → default (2048)
		Secret:     m.Secret,
		NoZeroCopy: m.NoZeroCopy,
		UseDma:     m.UseDma,
		HwAddr:     m.HwAddr,
	})
	if err != nil {
		return fmt.Errorf("memif create %s: %w", m.Socket, err)
	}
	if err := vppinterface.SetState(d.ch, swIfIndex, interface_types.IF_STATUS_API_FLAG_ADMIN_UP); err != nil {
		return fmt.Errorf("memif set-up %s: %w", m.Socket, err)
	}
	if m.MTU > 0 {
		if err := vppinterface.SetMtu(d.ch, swIfIndex, m.MTU); err != nil {
			return fmt.Errorf("memif set-mtu %s: %w", m.Socket, err)
		}
	}
	if m.BridgeID != 0 {
		// AddBridgeInterface creates the bridge-domain if it does not exist.
		if err := vppbridge.AddBridgeInterface(d.ch, m.BridgeID, swIfIndex); err != nil {
			return fmt.Errorf("memif bridge add (bd %d): %w", m.BridgeID, err)
		}
	}
	return nil
}

func (d *vppDataplane) DeleteMaster(swIfIndex uint32) error {
	// DeleteMemifInterface is idempotent (an already-absent memif is a no-op).
	if err := vppmemif.DeleteMemifInterface(d.ch, interface_types.InterfaceIndex(swIfIndex)); err != nil {
		return fmt.Errorf("memif delete (swIfIndex %d): %w", swIfIndex, err)
	}
	return nil
}

func (d *vppDataplane) dumpSockets() (map[uint32]string, error) {
	socks := map[uint32]string{}
	req := d.ch.SendMultiRequest(&memif.MemifSocketFilenameDump{})
	for {
		det := &memif.MemifSocketFilenameDetails{}
		stop, err := req.ReceiveReply(det)
		if err != nil {
			return nil, fmt.Errorf("memif socket dump: %w", err)
		}
		if stop {
			break
		}
		socks[det.SocketID] = strings.TrimRight(string(det.SocketFilename), "\x00")
	}
	return socks, nil
}

func parseMode(mode string) memif.MemifMode {
	switch strings.ToLower(mode) {
	case "ip":
		return memif.MEMIF_MODE_API_IP
	case "punt-inject", "inject-punt":
		return memif.MEMIF_MODE_API_PUNT_INJECT
	default:
		return memif.MEMIF_MODE_API_ETHERNET
	}
}

func modeString(mode memif.MemifMode) string {
	switch mode {
	case memif.MEMIF_MODE_API_IP:
		return "ip"
	case memif.MEMIF_MODE_API_PUNT_INJECT:
		return "punt-inject"
	default:
		return "ethernet"
	}
}

var _ Dataplane = (*vppDataplane)(nil)

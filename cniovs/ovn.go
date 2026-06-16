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

// OVN integration for the OVS-DPDK engine.
//
// When a pod attachment carries an OvnPort (the UUID of a pre-existing
// Neutron/OVN logical port), the dpdkvhostuser port created on the OVS
// integration bridge is tagged with external_ids:iface-id=<OvnPort>.
// ovn-controller then claims the logical port for this chassis and programs
// the datapath. The Neutron-assigned MAC is propagated to the in-pod DPDK app
// (via the saved config data), since that app — not a kernel netdev — sources
// the traffic OVN's flows match on.
//
// Resolution mirrors kubevirt/ovs-cni: CNI_ARGS env first, then a fallback to
// the per-pod cni-args carried in StdinData (args.cni.<key>), which is how
// multus-cni propagates a pod-annotation OvnPort.

package cniovs

import (
	"encoding/json"
	"strings"

	"github.com/containernetworking/cni/pkg/skel"
	cnitypes "github.com/containernetworking/cni/pkg/types"

	"github.com/intel/userspace-cni-network-plugin/pkg/types"
)

// ovnIntegrationBridge is the default OVS bridge an OVN-bound port attaches to
// when no bridge is explicitly configured.
const ovnIntegrationBridge = "br-int"

// envArgs captures the values that may arrive via the CNI_ARGS env string
// (e.g. CNI_ARGS=OvnPort=...;MAC=...).
type envArgs struct {
	cnitypes.CommonArgs
	MAC     cnitypes.UnmarshallableString `json:"mac,omitempty"`
	OvnPort cnitypes.UnmarshallableString `json:"ovnPort,omitempty"`
}

// resolveOvnArgs returns the OVN logical port id (OvnPort) and the requested
// MAC for this attachment. Either value may be "" when not supplied.
func resolveOvnArgs(conf *types.NetConf, args *skel.CmdArgs) (ovnPort, mac string) {
	if args != nil && args.Args != "" {
		e := envArgs{}
		if err := cnitypes.LoadArgs(args.Args, &e); err == nil {
			ovnPort = string(e.OvnPort)
			mac = string(e.MAC)
		}
	}
	// multus propagates pod-annotation cni-args through StdinData, not CNI_ARGS,
	// so fill anything still missing from conf.Args.Cni.
	if ovnPort == "" || mac == "" {
		applyConfArgsFallback(conf, &mac, &ovnPort)
	}
	return ovnPort, mac
}

// applyConfArgsFallback fills mac/ovnPort from conf.Args.Cni (StdinData) when
// the CNI_ARGS env did not provide them. Key matching is case-insensitive
// ("OvnPort", "ovnPort", "ovnport" all work). Non-string values are skipped.
func applyConfArgsFallback(conf *types.NetConf, mac, ovnPort *string) {
	if conf == nil || conf.Args == nil || conf.Args.Cni == nil {
		return
	}
	wantMAC := mac != nil && *mac == ""
	wantOvnPort := ovnPort != nil && *ovnPort == ""
	if !wantMAC && !wantOvnPort {
		return
	}
	for k, raw := range conf.Args.Cni {
		switch strings.ToLower(k) {
		case "mac":
			if wantMAC {
				if v, ok := decodeArgString(raw); ok {
					*mac = v
					wantMAC = false
				}
			}
		case "ovnport":
			if wantOvnPort {
				if v, ok := decodeArgString(raw); ok {
					*ovnPort = v
					wantOvnPort = false
				}
			}
		}
		if !wantMAC && !wantOvnPort {
			return
		}
	}
}

func decodeArgString(raw json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

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
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/intel/userspace-cni-network-plugin/pkg/annotations"
	"github.com/intel/userspace-cni-network-plugin/pkg/types"
)

// netAnnotKey is Multus's network-attachment annotation.
const netAnnotKey = "k8s.v1.cni.cncf.io/networks"

// networkRef identifies a NAD attached to a pod, plus the in-pod interface name
// Multus assigns it (when the annotation specifies one).
type networkRef struct {
	Namespace string
	Name      string
	Interface string
}

// parseNetworks parses the k8s.v1.cni.cncf.io/networks annotation (Multus),
// supporting the comma-separated short form ("[ns/]name[@iface]") and the JSON
// array form. defaultNS applies when an entry omits the namespace.
func parseNetworks(annotation, defaultNS string) []networkRef {
	annotation = strings.TrimSpace(annotation)
	if annotation == "" {
		return nil
	}
	if strings.HasPrefix(annotation, "[") {
		var arr []struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
			Interface string `json:"interface"`
		}
		if err := json.Unmarshal([]byte(annotation), &arr); err != nil {
			return nil
		}
		var out []networkRef
		for _, e := range arr {
			if e.Name == "" {
				continue
			}
			ns := e.Namespace
			if ns == "" {
				ns = defaultNS
			}
			out = append(out, networkRef{Namespace: ns, Name: e.Name, Interface: e.Interface})
		}
		return out
	}
	var out []networkRef
	for _, tok := range strings.Split(annotation, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		var iface string
		if i := strings.Index(tok, "@"); i >= 0 {
			iface = tok[i+1:]
			tok = tok[:i]
		}
		ns, name := defaultNS, tok
		if i := strings.Index(tok, "/"); i >= 0 {
			ns, name = tok[:i], tok[i+1:]
		}
		if name == "" {
			continue
		}
		out = append(out, networkRef{Namespace: ns, Name: name, Interface: iface})
	}
	return out
}

// bridgeDomainID resolves a host BridgeConf to a VPP bridge-domain id, matching
// cnivpp: a numeric bridgeName takes precedence, else bridgeId.
func bridgeDomainID(b types.BridgeConf) (uint32, error) {
	if b.BridgeName != "" {
		id, err := strconv.ParseUint(b.BridgeName, 10, 32)
		if err != nil {
			return 0, fmt.Errorf("bridgeName %q is not a numeric bridge id: %w", b.BridgeName, err)
		}
		return uint32(id), nil
	}
	return uint32(b.BridgeId), nil
}

// socketFileName returns the host socket filename, matching cnivpp's
// getMemifSocketfileName: the host MemifConf.Socketfile if set, else
// memif-<containerID[:12]>-<ifName>.sock.
func socketFileName(e types.ConfigurationData, host types.UserSpaceConf) string {
	if host.MemifConf.Socketfile != "" {
		return host.MemifConf.Socketfile
	}
	cid := e.ContainerId
	if len(cid) > 12 {
		cid = cid[:12]
	}
	return fmt.Sprintf("memif-%s-%s.sock", cid, e.IfName)
}

// desiredMemif builds the host memif master for one configuration-data entry,
// given the NAD host config it belongs to and the resolved host shared dir.
// ok=false when this interface is not a VPP memif we reconcile.
func desiredMemif(e types.ConfigurationData, host types.UserSpaceConf, hostSharedDir string) (Memif, bool, error) {
	if !strings.EqualFold(host.IfType, "memif") {
		return Memif{}, false, nil
	}
	if host.Engine != "" && !strings.EqualFold(host.Engine, "vpp") {
		return Memif{}, false, nil
	}
	var bdID uint32
	if strings.EqualFold(host.NetType, "bridge") {
		id, err := bridgeDomainID(host.BridgeConf)
		if err != nil {
			return Memif{}, false, err
		}
		bdID = id
	}
	mode := host.MemifConf.Mode
	if mode == "" {
		mode = e.Config.MemifConf.Mode
	}
	return Memif{
		Socket:   path.Join(hostSharedDir, socketFileName(e, host)),
		ID:       0, // userspace-cni creates the pair with memif id 0
		BridgeID: bdID,
		Mode:     mode,
	}, true, nil
}

// desiredForPod builds every host memif master a pod declares. nadConfigJSON
// returns a NAD's spec.config given its namespace+name (the caller fetches it).
// A pod with no userspace networks, no configuration-data, or no shared-dir
// volume yields nothing.
func desiredForPod(pod *corev1.Pod, nadConfigJSON func(ns, name string) (string, error)) ([]Memif, error) {
	nets := parseNetworks(pod.Annotations[netAnnotKey], pod.Namespace)
	if len(nets) == 0 {
		return nil, nil
	}
	raw := pod.Annotations[annotations.AnnotKeyUsrspConfigData]
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var entries []types.ConfigurationData
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("parse %s: %w", annotations.AnnotKeyUsrspConfigData, err)
	}
	if len(entries) == 0 {
		return nil, nil
	}
	sharedDir, err := annotations.GetPodVolumeMountHostSharedDir(pod)
	if err != nil || sharedDir == "" {
		// No shared-dir volume → userspace-cni could not have created a socket here.
		return nil, nil
	}

	// Parse each attached NAD's host config once.
	hosts := make([]types.UserSpaceConf, len(nets))
	for i, n := range nets {
		cfg, err := nadConfigJSON(n.Namespace, n.Name)
		if err != nil {
			return nil, fmt.Errorf("nad %s/%s: %w", n.Namespace, n.Name, err)
		}
		var nc types.NetConf
		if err := json.Unmarshal([]byte(cfg), &nc); err != nil {
			return nil, fmt.Errorf("parse NAD %s/%s config: %w", n.Namespace, n.Name, err)
		}
		hosts[i] = nc.HostConf
	}

	if len(nets) == 1 {
		return buildDesired(entries, hosts[0], sharedDir)
	}

	// Multi-network: match each entry to its network by interface name.
	var out []Memif
	for _, e := range entries {
		for i, n := range nets {
			if n.Interface != "" && n.Interface != e.IfName {
				continue
			}
			m, ok, err := desiredMemif(e, hosts[i], sharedDir)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, m)
			}
			break
		}
	}
	return out, nil
}

// buildDesired builds the desired host memif masters for the entries belonging
// to one NAD host config, under hostSharedDir. Non-memif entries are skipped.
func buildDesired(entries []types.ConfigurationData, host types.UserSpaceConf, hostSharedDir string) ([]Memif, error) {
	var out []Memif
	for _, e := range entries {
		m, ok, err := desiredMemif(e, host, hostSharedDir)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, m)
		}
	}
	return out, nil
}

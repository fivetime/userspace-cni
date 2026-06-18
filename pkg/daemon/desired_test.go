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
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/intel/userspace-cni-network-plugin/pkg/annotations"
	"github.com/intel/userspace-cni-network-plugin/pkg/types"
)

// Real-shape fixtures (userspace-cni pkg/types + a userspace NAD).
const sampleConfigData = `[{"containerId":"0958c8871b32abcd","ifName":"net1",` +
	`"name":"userspace-vpp-net-1","config":{"iftype":"memif","netType":"interface",` +
	`"memif":{"role":"slave","mode":"ethernet"}}}]`

const sampleNAD = `{"cniVersion":"1.0.0","type":"userspace","name":"userspace-vpp-net-1",` +
	`"host":{"engine":"vpp","iftype":"memif","netType":"bridge",` +
	`"memif":{"role":"master","mode":"ethernet"},"bridge":{"bridgeName":"100"}},` +
	`"container":{"engine":"vpp","iftype":"memif","netType":"interface",` +
	`"memif":{"role":"slave","mode":"ethernet"}}}`

func TestParseNetworks(t *testing.T) {
	tests := []struct {
		name, anno, ns string
		want           []networkRef
	}{
		{"simple", "userspace-vpp-net-1", "vpp",
			[]networkRef{{Namespace: "vpp", Name: "userspace-vpp-net-1"}}},
		{"ns + iface", "blue/net-a@net1", "vpp",
			[]networkRef{{Namespace: "blue", Name: "net-a", Interface: "net1"}}},
		{"comma list", "a, b/c", "vpp",
			[]networkRef{{Namespace: "vpp", Name: "a"}, {Namespace: "b", Name: "c"}}},
		{"json", `[{"name":"net-a","namespace":"blue"},{"name":"net-b"}]`, "vpp",
			[]networkRef{{Namespace: "blue", Name: "net-a"}, {Namespace: "vpp", Name: "net-b"}}},
		{"empty", "", "vpp", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseNetworks(tc.anno, tc.ns); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseNetworks(%q) = %+v, want %+v", tc.anno, got, tc.want)
			}
		})
	}
}

func TestBridgeDomainID(t *testing.T) {
	if id, err := bridgeDomainID(types.BridgeConf{BridgeName: "100"}); err != nil || id != 100 {
		t.Errorf("bridgeName 100 = %d, %v", id, err)
	}
	if _, err := bridgeDomainID(types.BridgeConf{BridgeName: "br-0"}); err == nil {
		t.Error("non-numeric bridgeName should error")
	}
	if id, err := bridgeDomainID(types.BridgeConf{BridgeId: 42}); err != nil || id != 42 {
		t.Errorf("fallback bridgeId = %d, %v", id, err)
	}
}

func TestSocketFileName(t *testing.T) {
	e := types.ConfigurationData{ContainerId: "0958c8871b32abcd", IfName: "net1"}
	if got := socketFileName(e, types.UserSpaceConf{}); got != "memif-0958c8871b32-net1.sock" {
		t.Errorf("derived = %q", got)
	}
	host := types.UserSpaceConf{MemifConf: types.MemifConf{Socketfile: "custom.sock"}}
	if got := socketFileName(e, host); got != "custom.sock" {
		t.Errorf("explicit = %q", got)
	}
}

func TestDesiredForPod(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "vpp",
			Annotations: map[string]string{
				netAnnotKey:                        "userspace-vpp-net-1",
				annotations.AnnotKeyUsrspConfigData: sampleConfigData,
			},
		},
		Spec: corev1.PodSpec{Volumes: []corev1.Volume{{
			Name:         "shared-dir",
			VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/run/vpp/app1"}},
		}}},
	}
	lookup := func(ns, name string) (string, error) {
		if ns == "vpp" && name == "userspace-vpp-net-1" {
			return sampleNAD, nil
		}
		return "", fmt.Errorf("nad %s/%s not found", ns, name)
	}

	got, err := desiredForPod(pod, lookup)
	if err != nil {
		t.Fatal(err)
	}
	want := []Memif{{Socket: "/run/vpp/app1/memif-0958c8871b32-net1.sock", BridgeID: 100, Mode: "ethernet"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("desiredForPod = %+v, want %+v", got, want)
	}

	// A pod without the userspace annotations yields nothing.
	bare := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "vpp"}}
	if got, err := desiredForPod(bare, lookup); err != nil || got != nil {
		t.Errorf("bare pod: got %+v, err %v", got, err)
	}
}

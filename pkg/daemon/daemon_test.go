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
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/intel/userspace-cni-network-plugin/pkg/annotations"
)

type fakePods struct{ pods []corev1.Pod }

func (f fakePods) ListNodePods(context.Context) ([]corev1.Pod, error) { return f.pods, nil }

type fakeNADs struct{ cfg map[string]string }

func (f fakeNADs) GetNADConfig(_ context.Context, ns, name string) (string, error) {
	if c, ok := f.cfg[ns+"/"+name]; ok {
		return c, nil
	}
	return "", fmt.Errorf("nad %s/%s not found", ns, name)
}

func memifPod() corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "vpp", Name: "app1",
			Annotations: map[string]string{
				netAnnotKey:                         "userspace-vpp-net-1",
				annotations.AnnotKeyUsrspConfigData: sampleConfigData,
			},
		},
		Spec: corev1.PodSpec{Volumes: []corev1.Volume{{
			Name:         "shared-dir",
			VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/run/vpp/app1"}},
		}}},
	}
}

func TestReconcilerSync(t *testing.T) {
	dp := &fakeDataplane{} // VPP empty → the pod's master is missing → create it.
	r := &Reconciler{
		Pods: fakePods{pods: []corev1.Pod{memifPod(), {ObjectMeta: metav1.ObjectMeta{Name: "no-net"}}}},
		NADs: fakeNADs{cfg: map[string]string{"vpp/userspace-vpp-net-1": sampleNAD}},
		DP:   dp,
	}

	created, deleted, err := r.Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 || deleted != 0 {
		t.Fatalf("created=%d deleted=%d, want 1/0", created, deleted)
	}
	if len(dp.created) != 1 || dp.created[0].Socket != "/run/vpp/app1/memif-0958c8871b32-net1.sock" ||
		dp.created[0].BridgeID != 100 {
		t.Errorf("created memif = %+v", dp.created)
	}
}

func TestReconcilerSyncSkipsBadPod(t *testing.T) {
	// A good pod (valid NAD) alongside a bad one (missing NAD). The bad pod is
	// skipped — Sync does not error and still reconciles the good pod. (GC stays
	// safe via GCOrphan/SocketGone, exercised separately.)
	dp := &fakeDataplane{} // VPP empty → the good pod's master gets created.
	good := memifPod()
	bad := memifPod()
	bad.Name = "app-bad"
	bad.Annotations[netAnnotKey] = "missing-nad"
	r := &Reconciler{
		Pods: fakePods{pods: []corev1.Pod{good, bad}},
		NADs: fakeNADs{cfg: map[string]string{"vpp/userspace-vpp-net-1": sampleNAD}},
		DP:   dp,
	}
	created, _, err := r.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync should skip the bad pod, not fail: %v", err)
	}
	if created != 1 {
		t.Fatalf("created=%d, want 1 (good pod created; bad pod skipped)", created)
	}
}

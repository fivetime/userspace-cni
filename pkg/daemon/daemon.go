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
	"os"

	corev1 "k8s.io/api/core/v1"
)

// SocketGone reports whether a memif's host socket file is absent. The daemon
// uses it as the GC guard: CNI ADD creates the socket and CNI DEL removes it, so
// a missing socket means CNI has fully torn the memif down (and VPP merely leaked
// the master) — safe to GC. A present socket means the memif is live or a CNI ADD
// is still in flight, so GC is skipped. ("Whoever creates it destroys it.")
func SocketGone(m Memif) bool {
	_, err := os.Stat(m.Socket)
	return os.IsNotExist(err)
}

// PodLister lists the pods scheduled on this node. The concrete implementation
// uses a node-scoped client-go list; tests use a fake.
type PodLister interface {
	ListNodePods(ctx context.Context) ([]corev1.Pod, error)
}

// NADConfigGetter returns a NetworkAttachmentDefinition's spec.config JSON.
type NADConfigGetter interface {
	GetNADConfig(ctx context.Context, namespace, name string) (string, error)
}

// Reconciler ties the pods' declared memif intent to the node VPP dataplane.
// It is the orchestration the connection watcher calls on each (re)connect.
type Reconciler struct {
	Pods PodLister
	NADs NADConfigGetter
	DP   Dataplane
	// GCOrphan gates orphan deletion (see Reconcile). nil deletes every orphan;
	// the daemon sets it to SocketGone.
	GCOrphan func(Memif) bool
}

// Sync builds the desired set of memif masters from the live node-local pods and
// converges the dataplane to it. It aborts (without touching the dataplane) if
// any pod's intent cannot be evaluated — proceeding on incomplete desired could
// misclassify a live memif as an orphan and delete it.
func (r *Reconciler) Sync(ctx context.Context) (created, deleted int, err error) {
	pods, err := r.Pods.ListNodePods(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("list node pods: %w", err)
	}
	var desired []Memif
	for i := range pods {
		nadLookup := func(ns, name string) (string, error) {
			return r.NADs.GetNADConfig(ctx, ns, name)
		}
		ms, perr := desiredForPod(&pods[i], nadLookup)
		if perr != nil {
			return 0, 0, fmt.Errorf("pod %s/%s: %w", pods[i].Namespace, pods[i].Name, perr)
		}
		desired = append(desired, ms...)
	}
	return Reconcile(r.DP, desired, r.GCOrphan)
}

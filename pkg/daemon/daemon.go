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

	"github.com/intel/userspace-cni-network-plugin/logging"
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
	// Status, if set, receives connection/reconcile observability updates.
	Status *Status
}

// Sync builds the desired set of memif masters from the live node-local pods and
// converges the dataplane to it.
//
// A pod whose intent cannot be evaluated (e.g. its NAD is transiently unreadable)
// is logged and SKIPPED rather than aborting the whole node's reconcile — at
// scale (e.g. a boot storm) one bad pod must not block every other pod's restore.
// Skipping a pod is safe for GC because deletion is gated by GCOrphan (SocketGone):
// a skipped-but-live pod keeps its socket file, so its memif is never GC'd; it is
// simply restored on a later reconcile once its intent is readable.
func (r *Reconciler) Sync(ctx context.Context) (created, deleted int, err error) {
	pods, err := r.Pods.ListNodePods(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("list node pods: %w", err)
	}
	// Per-reconcile NAD cache: many pods share a NAD, so memoize the Gets (and
	// their errors) for this Sync to avoid O(pods) duplicate API calls at scale.
	nadCache := map[string]string{}
	nadErr := map[string]error{}
	nadLookup := func(ns, name string) (string, error) {
		key := ns + "/" + name
		if e, ok := nadErr[key]; ok {
			return "", e
		}
		if c, ok := nadCache[key]; ok {
			return c, nil
		}
		cfg, gerr := r.NADs.GetNADConfig(ctx, ns, name)
		if gerr != nil {
			nadErr[key] = gerr
			return "", gerr
		}
		nadCache[key] = cfg
		return cfg, nil
	}

	var desired []Memif
	for i := range pods {
		ms, perr := desiredForPod(&pods[i], nadLookup)
		if perr != nil {
			logging.Warningf("restore-daemon: skipping pod %s/%s: %v", pods[i].Namespace, pods[i].Name, perr)
			continue
		}
		desired = append(desired, ms...)
	}
	return Reconcile(r.DP, desired, r.GCOrphan)
}

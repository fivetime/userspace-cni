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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// nadGVR is the NetworkAttachmentDefinition resource (read as unstructured so we
// need no typed NAD dependency — we only want spec.config).
var nadGVR = schema.GroupVersionResource{
	Group:    "k8s.cni.cncf.io",
	Version:  "v1",
	Resource: "network-attachment-definitions",
}

// K8sPodLister lists the pods scheduled on a node via a node-scoped field selector.
type K8sPodLister struct {
	Client   kubernetes.Interface
	NodeName string
}

func (l K8sPodLister) ListNodePods(ctx context.Context) ([]corev1.Pod, error) {
	sel := fields.OneTermEqualSelector("spec.nodeName", l.NodeName).String()
	list, err := l.Client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{FieldSelector: sel})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

// K8sNADGetter fetches a NetworkAttachmentDefinition's spec.config via the dynamic client.
type K8sNADGetter struct {
	Dyn dynamic.Interface
}

func (g K8sNADGetter) GetNADConfig(ctx context.Context, namespace, name string) (string, error) {
	obj, err := g.Dyn.Resource(nadGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	cfg, found, err := unstructured.NestedString(obj.Object, "spec", "config")
	if err != nil {
		return "", err
	}
	if !found || cfg == "" {
		return "", fmt.Errorf("NAD %s/%s has no spec.config", namespace, name)
	}
	return cfg, nil
}

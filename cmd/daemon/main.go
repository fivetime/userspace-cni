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

// Command userspace-daemon is the per-node restore daemon: it watches the node
// VPP connection and, on (re)connect, re-asserts the memif masters userspace-cni
// owns (VPP loses them on restart). See docs/proposals/vpp-memif-restore-daemon.md.
// It runs as a pod (the userspace-cni DaemonSet), so it uses its in-cluster
// ServiceAccount — no on-host kubeconfig.
package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/intel/userspace-cni-network-plugin/logging"
	"github.com/intel/userspace-cni-network-plugin/pkg/daemon"
)

func main() {
	var apiSocket, socketPrefix, logLevel, metricsAddr string
	var gcGrace int
	flag.StringVar(&apiSocket, "vpp-api-socket", "",
		"VPP binary API socket (empty = govpp default /run/vpp/api.sock)")
	flag.StringVar(&socketPrefix, "socket-prefix", "",
		"only manage memif masters whose socket is under this prefix (comma-separated; empty = all memifs)")
	flag.StringVar(&logLevel, "log-level", "info",
		"log level (verbose|debug|info|warning|error|panic)")
	flag.StringVar(&metricsAddr, "metrics-addr", ":9101",
		"listen address for /healthz, /readyz and /metrics")
	flag.IntVar(&gcGrace, "gc-grace", 2,
		"consecutive reconciles a socket-present orphan must persist before GC (0 = only GC once its socket is gone)")
	flag.Parse()

	// Log to stderr at the requested level so `kubectl logs` surfaces the
	// daemon's activity (the cni-log default is quiet / file-oriented).
	logging.SetLogStderr(true)
	logging.SetLogLevel(logLevel)

	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		logging.Errorf("restore-daemon: NODE_NAME env is required (set it from the downward API)")
		os.Exit(1)
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		logging.Errorf("restore-daemon: in-cluster config: %v", err)
		os.Exit(1)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		logging.Errorf("restore-daemon: kubernetes client: %v", err)
		os.Exit(1)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		logging.Errorf("restore-daemon: dynamic client: %v", err)
		os.Exit(1)
	}

	status := &daemon.Status{}
	r := &daemon.Reconciler{
		Pods:   daemon.K8sPodLister{Client: clientset, NodeName: nodeName},
		NADs:   daemon.K8sNADGetter{Dyn: dyn},
		Grace:  gcGrace,
		Status: status,
	}

	go func() {
		logging.Infof("restore-daemon: serving health/metrics on %s", metricsAddr)
		if err := http.ListenAndServe(metricsAddr, status.Handler()); err != nil {
			logging.Errorf("restore-daemon: metrics server stopped: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logging.Infof("restore-daemon: starting on node %s (vpp-api-socket=%q, socket-prefix=%q)",
		nodeName, apiSocket, socketPrefix)
	if err := r.Run(ctx, apiSocket, socketPrefix); err != nil && err != context.Canceled {
		logging.Errorf("restore-daemon: exited with error: %v", err)
		os.Exit(1)
	}
	logging.Infof("restore-daemon: stopped")
}

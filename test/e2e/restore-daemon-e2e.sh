#!/usr/bin/env bash
#
# Copyright(c) 2026 The userspace-cni Authors.
# SPDX-License-Identifier: Apache-2.0
#
# End-to-end test for the VPP memif restore daemon
# (docs/proposals/vpp-memif-restore-daemon.md).
#
# It does NOT run in plain CI: it needs a real cluster with a shared VPP (owned
# by some per-node operator), userspace-cni deployed with restoreDaemon.enabled,
# a userspace NAD, and at least two memif workload pods already wired and able to
# ping each other. It then kills the node's VPP and asserts the daemon
# automatically rebuilds the memifs — without recreating the workload pods.
#
# Configure via env (defaults match the bird-k8s reference setup):
#   NODE         node under test
#   VPP_NS/VPP_LABEL        the pod that runs the node VPP (killed to simulate a restart)
#   VPP_CONTAINER          VPP container name in that pod
#   DAEMON_NS/DAEMON_LABEL  the userspace-cni DaemonSet pod
#   APP_NS/APP1/APP2        the two memif workload pods
#   PING_FROM/PING_TARGET   ping app (exec'd vppctl) and its peer IP
#   WAIT                    seconds to wait for VPP+daemon to converge
set -euo pipefail

NODE="${NODE:-network1}"
VPP_NS="${VPP_NS:-bird-k8s-system}"
VPP_LABEL="${VPP_LABEL:-app=bird-k8s}"
VPP_CONTAINER="${VPP_CONTAINER:-vpp}"
DAEMON_NS="${DAEMON_NS:-kube-system}"
DAEMON_LABEL="${DAEMON_LABEL:-app.kubernetes.io/name=userspace-cni}"
APP_NS="${APP_NS:-vpp}"
APP1="${APP1:-vpp-app1}"
APP2="${APP2:-vpp-app2}"
PING_FROM="${PING_FROM:-$APP1}"
PING_TARGET="${PING_TARGET:-192.168.1.4}"
WAIT="${WAIT:-90}"

fail() { echo "FAIL: $*" >&2; exit 1; }
note() { echo "=== $* ==="; }

vpp_pod() { kubectl -n "$VPP_NS" get pod -l "$VPP_LABEL" --field-selector "spec.nodeName=$NODE" -o jsonpath='{.items[0].metadata.name}'; }
daemon_pod() { kubectl -n "$DAEMON_NS" get pod -l "$DAEMON_LABEL" --field-selector "spec.nodeName=$NODE" -o jsonpath='{.items[0].metadata.name}'; }
memif_count() { kubectl -n "$VPP_NS" exec "$(vpp_pod)" -c "$VPP_CONTAINER" -- vppctl show interface | grep -c -i memif || true; }
restarts() { kubectl -n "$APP_NS" get pod "$1" -o jsonpath='{.status.containerStatuses[0].restartCount}'; }
ping_ok() { kubectl -n "$APP_NS" exec "$PING_FROM" -- vppctl ping "$PING_TARGET" repeat 3 | grep -q "received"; }

note "baseline: ping + memif count + app restart counts"
ping_ok || fail "baseline ping $PING_FROM -> $PING_TARGET did not work; set up the apps first"
base_memifs="$(memif_count)"
r1="$(restarts "$APP1")"; r2="$(restarts "$APP2")"
echo "baseline: memifs=$base_memifs ${APP1} restarts=$r1 ${APP2} restarts=$r2"
[ "$base_memifs" -ge 2 ] || fail "expected >=2 memifs at baseline, got $base_memifs"

note "kill the node VPP pod ($(vpp_pod)) to simulate a VPP restart"
kubectl -n "$VPP_NS" delete pod "$(vpp_pod)" --wait=false
echo "waiting ${WAIT}s for VPP to come back and the daemon to reconcile..."
sleep "$WAIT"
kubectl -n "$VPP_NS" wait --for=condition=Ready "pod/$(vpp_pod)" --timeout=60s

note "assert: daemon reconciled (created), memifs restored, ping recovered, apps NOT restarted"
dpod="$(daemon_pod)"
kubectl -n "$DAEMON_NS" logs "$dpod" -c restore-daemon | grep -q "reconcile done" \
  || fail "no 'reconcile done' in restore-daemon log"
echo "daemon log tail:"; kubectl -n "$DAEMON_NS" logs "$dpod" -c restore-daemon | grep -iE "connected|reconcile done" | tail -3

now_memifs="$(memif_count)"
[ "$now_memifs" -ge "$base_memifs" ] || fail "memifs not restored: have $now_memifs, want >=$base_memifs"

[ "$(restarts "$APP1")" = "$r1" ] || fail "$APP1 was restarted (restore should not touch workload pods)"
[ "$(restarts "$APP2")" = "$r2" ] || fail "$APP2 was restarted (restore should not touch workload pods)"

ping_ok || fail "ping did not recover after the VPP restart"

echo
echo "PASS: VPP restarted, daemon restored $now_memifs memif(s), ping recovered, apps not restarted."

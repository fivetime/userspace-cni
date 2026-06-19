#!/usr/bin/env bash
#
# verify-ovn-dpdk-binding.sh
#
# Minimal data-plane verification for the "OvnPort -> external_ids:iface-id"
# pattern on an OVS-DPDK + OVN node, BEFORE wiring OvnPort support into cniovs.
#
# It manually reproduces what cniovs would do for one port:
#   1. sanity-checks the OVS-DPDK + OVN environment,
#   2. creates a dpdkvhostuser(client) port on br-int with iface-id set to a
#      pre-existing Neutron/OVN logical port UUID,
#   3. confirms ovn-controller BINDS the logical port to THIS chassis and
#      installs OpenFlow flows for it.
#
# Steps 4-5 (start a DPDK app that uses the Neutron-assigned MAC, then run a
# traffic test) are environment-specific and left as guided manual steps — they
# are the part that proves the userspace-specific premise: the in-pod app must
# source traffic with the Neutron MAC/IP or OVN drops it.
#
# Run on the target node as root. This does NOT touch cniovs or Kubernetes.
#
# Usage:
#   NEUTRON_PORT_UUID=<uuid> NEUTRON_MAC=<aa:bb:..> [NEUTRON_IP=10.0.0.8/24] \
#     ./verify-ovn-dpdk-binding.sh
#
set -u

# ---- Inputs ----------------------------------------------------------------
NEUTRON_PORT_UUID="${NEUTRON_PORT_UUID:-}"   # required: existing OVN logical port UUID
NEUTRON_MAC="${NEUTRON_MAC:-}"               # required: MAC Neutron assigned to that port
NEUTRON_IP="${NEUTRON_IP:-}"                 # optional: IP/prefix for the traffic-test hint

BRIDGE="${BRIDGE:-br-int}"
PORT_NAME="${PORT_NAME:-vrfy-dpdk0}"
SOCK_DIR="${SOCK_DIR:-/var/run/vpp_sockets}"
SOCK_PATH="${SOCK_DIR}/${PORT_NAME}.sock"

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "PASS: $*"; }
have() { command -v "$1" >/dev/null 2>&1; }

[ "$(id -u)" = "0" ] || fail "run as root"
[ -n "$NEUTRON_PORT_UUID" ] || fail "set NEUTRON_PORT_UUID=<existing OVN logical port uuid>"
[ -n "$NEUTRON_MAC" ] || fail "set NEUTRON_MAC=<mac Neutron assigned to that port>"
have ovs-vsctl || fail "ovs-vsctl not found"
have ovs-ofctl || fail "ovs-ofctl not found"

# ---- Step 1: environment sanity -------------------------------------------
echo "== Step 1: environment sanity =="

ovs-vsctl br-exists "$BRIDGE" || fail "bridge $BRIDGE does not exist (OVN integration bridge expected)"

dp_type="$(ovs-vsctl --no-heading --columns=datapath_type find bridge name="$BRIDGE" | tr -d '\" ')"
[ "$dp_type" = "netdev" ] || fail "$BRIDGE datapath_type=$dp_type (need 'netdev' for OVS-DPDK; dpdkvhostuser ports cannot attach to a kernel datapath)"
ok "$BRIDGE is datapath_type=netdev (OVS-DPDK)"

# ovn-controller must be running and connected for binding to happen.
if have ovn-sbctl; then
  ovn-sbctl --columns=_uuid list Chassis >/dev/null 2>&1 || echo "WARN: cannot reach OVN Southbound via ovn-sbctl (binding check below may be limited)"
else
  echo "WARN: ovn-sbctl not on this node; will fall back to checking Interface ofport/flows only"
fi

# Pre-existing logical port?
if have ovn-sbctl; then
  if ! ovn-sbctl --bare --columns=logical_port find Port_Binding logical_port="$NEUTRON_PORT_UUID" | grep -q .; then
    echo "WARN: Port_Binding for logical_port=$NEUTRON_PORT_UUID not found in Southbound."
    echo "      Make sure the Neutron port exists and is on a logical switch bound to this OVN."
  fi
fi

mkdir -p "$SOCK_DIR"

# ---- Step 2: create the dpdkvhostuser port with iface-id ------------------
echo "== Step 2: create dpdkvhostuserclient port on $BRIDGE with iface-id=$NEUTRON_PORT_UUID =="

cleanup() {
  echo "== Cleanup: removing $PORT_NAME from $BRIDGE =="
  ovs-vsctl --if-exists del-port "$BRIDGE" "$PORT_NAME"
  rm -f "$SOCK_PATH"
}
trap cleanup EXIT

# Mirrors what cniovs createVhostPort would emit, plus external_ids:iface-id
# (the OvnPort glue). Client mode: OVS connects to the socket the app creates.
ovs-vsctl --may-exist add-port "$BRIDGE" "$PORT_NAME" \
  -- set Interface "$PORT_NAME" type=dpdkvhostuserclient \
       options:vhost-server-path="$SOCK_PATH" \
       external_ids:iface-id="$NEUTRON_PORT_UUID" \
  || fail "add-port failed"
ok "port $PORT_NAME created (type=dpdkvhostuserclient, iface-id set)"

# ---- Step 3: confirm ovn-controller bound it ------------------------------
echo "== Step 3: confirm OVN binding + flows =="

# Give ovn-controller a moment to react.
sleep 3

bound_ok=0
if have ovn-sbctl; then
  this_chassis="$(ovs-vsctl get Open_vSwitch . external_ids:system-id 2>/dev/null | tr -d '\"')"
  pb_chassis="$(ovn-sbctl --bare --columns=chassis find Port_Binding logical_port="$NEUTRON_PORT_UUID" 2>/dev/null)"
  echo "  this chassis system-id : ${this_chassis:-<unknown>}"
  echo "  Port_Binding.chassis   : ${pb_chassis:-<empty>}"
  if [ -n "$pb_chassis" ]; then
    ok "ovn-controller bound logical_port=$NEUTRON_PORT_UUID to a chassis"
    bound_ok=1
  else
    echo "WARN: Port_Binding has no chassis yet — not bound. Check ovn-controller logs and that iface-id matches the logical port UUID exactly."
  fi
fi

# Flow-level evidence: OVN installs flows referencing the port's ofport.
ofport="$(ovs-vsctl get Interface "$PORT_NAME" ofport 2>/dev/null | tr -d ' ')"
echo "  $PORT_NAME ofport       : ${ofport:-<none>}"
if [ -n "$ofport" ] && [ "$ofport" != "-1" ]; then
  nflows="$(ovs-ofctl -O OpenFlow13 dump-flows "$BRIDGE" 2>/dev/null | grep -c -E "in_port=$ofport|output:$ofport")"
  echo "  flows referencing ofport: ${nflows:-0}"
  if [ "${nflows:-0}" -gt 0 ]; then
    ok "OVN installed $nflows flow(s) referencing this port on $BRIDGE"
    bound_ok=1
  else
    echo "WARN: no flows reference ofport=$ofport yet (port may be down until the app connects the vhost socket)."
  fi
fi

echo
if [ "$bound_ok" = "1" ]; then
  echo "RESULT: control-plane binding looks GOOD."
else
  echo "RESULT: control-plane binding NOT confirmed — fix this before writing OvnPort into cniovs."
fi

# ---- Steps 4-5: app-side traffic test (manual) ----------------------------
cat <<EOF

== Steps 4-5 (manual): the userspace-specific premise ==

The control-plane binding above is necessary but NOT sufficient. In userspace
there is no kernel netdev carrying the MAC — the in-pod DPDK app MUST source
traffic with the Neutron-assigned MAC (and IP), or OVN port-security/flows drop
it. Verify with a DPDK app, e.g. testpmd:

  testpmd -l <cores> --vdev=net_virtio_user0,path=$SOCK_PATH,server=1,mac=$NEUTRON_MAC -- -i
EOF

if [ -n "$NEUTRON_IP" ]; then
  echo "  # configure $NEUTRON_IP inside the app stack"
fi

cat <<EOF

Then from another port on the SAME OVN logical switch, ping this port's IP and
confirm bidirectional traffic. If binding (Step 3) passed but traffic fails,
the MAC/IP the app uses almost certainly does not match the Neutron port.

Note: 'trap cleanup EXIT' will remove $PORT_NAME when this script exits. To keep
the port for the manual traffic test, run Steps 2-3 commands by hand instead.
EOF

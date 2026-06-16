# Advanced tuning examples

These NetworkAttachmentDefinitions demonstrate the optional tuning fields added
to the Userspace CNI config. All fields are optional and `omitempty` — existing
configs keep working unchanged.

| File | Engine | Shows |
|------|--------|-------|
| [vpp-memif-multiqueue.yaml](vpp-memif-multiqueue.yaml) | VPP | memif multi-queue + ring/buffer tuning + jumbo MTU |
| [vpp-vhostuser.yaml](vpp-vhostuser.yaml) | VPP | vhost-user interface (server mode) + virtio flags |
| [ovs-vlan-mtu.yaml](ovs-vlan-mtu.yaml) | OVS-DPDK | access VLAN tag + jumbo MTU |
| [ovn-vhostuser.yaml](ovn-vhostuser.yaml) | OVS-DPDK | bind to a Neutron/OVN logical port via `OvnPort` (iface-id) |

## Field reference

Top-level (per `host` / `container`):

| Field | Type | Engines | Notes |
|-------|------|---------|-------|
| `mtu` | int | OVS, VPP | Interface MTU. OVS sets `mtu_request`; VPP sets the L3 MTU. `0` = engine default. |
| `mac` | string | OVS, VPP | Fixed MAC `aa:bb:cc:dd:ee:ff`. VPP sets it on the interface (memif `hw_addr` / vhost `use_custom_mac`). OVS delivers it to the in-pod app via config data (not set on the dpdkvhostuser interface — the schema only honors `mac` for internal ports). For OVS, an annotation MAC (e.g. OvnPort) takes precedence over this field. Empty = random (OVS) / VPP-chosen. |

`memif` block (VPP only — ignored by ovs-dpdk):

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `rxQueues` | uint8 | 1 | RX queue count (multi-queue). Must match the peer. |
| `txQueues` | uint8 | 1 | TX queue count (multi-queue). Must match the peer. |
| `ringSize` | uint32 | 1024 | Descriptors per ring. Must be a power of 2. |
| `bufferSize` | uint16 | 2048 | Packet buffer size in bytes. |
| `secret` | string | "" | Optional shared secret, max 24 chars. |
| `noZeroCopy` | bool | false | Disable zero-copy mode. |
| `useDma` | bool | false | Enable DMA acceleration (memif_create_v2). |

`vhost` block (VPP-specific flags ignored by ovs-dpdk):

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `mode` | string | server (VPP) / server (OVS) | `client` or `server`. |
| `group` | string | "" | Socket file group ownership. OVS: applied to the shared dir. VPP: applied to the socket file in **server mode only** (in client mode VPP does not create the socket). |
| `enableGso` | bool | false | Generic segmentation offload (VPP). |
| `enablePacked` | bool | false | Packed virtqueue format (VPP). |
| `enableEventIdx` | bool | false | Virtio event index (VPP). |
| `ingressPolicingRate` | int | 0 | OVS only. Per-port ingress rate limit in kbps. `0` = no limit. |
| `ingressPolicingBurst` | int | 0 | OVS only. Ingress burst in kb (paired with the rate above). |

`bridge` block:

| Field | Type | Notes |
|-------|------|-------|
| `vlanId` | int | OVS only. Port `tag` — access VLAN, or native VLAN when combined with `trunks`. `0` = untagged. |
| `trunks` | []int | OVS only. Allowed VLAN ids for a trunk port. |
| `vlanMode` | string | OVS only. Explicit `vlan_mode`: access / trunk / native-tagged / native-untagged / dot1q-tunnel. Empty = OVS infers from `vlanId`/`trunks`. |

> Note: `rxQueues`/`txQueues` must be identical on both the `host` (master) and
> `container` (slave) ends — memif negotiates a fixed queue count.

## OVN integration (`OvnPort`)

For ovs-dpdk, a pod can bind its vhostuser port to a pre-existing Neutron/OVN
logical port by passing the logical port UUID as a `cni-args`. This is read
from the pod's network annotation (the same way kernel-mode `ovs-cni` does it):

```yaml
k8s.v1.cni.cncf.io/networks: |
  [{"name":"ovn-tenant","mac":"fa:16:3e:11:22:33","ips":["192.168.1.8/24"],
    "cni-args":{"OvnPort":"<neutron-logical-port-uuid>"}}]
```

When `OvnPort` is present the CNI:

- defaults the bridge to **br-int** if no `bridgeName` is set (and does not
  create it — OVN owns br-int),
- stamps **`external_ids:iface-id=<OvnPort>`** on the dpdkvhostuser interface so
  `ovn-controller` claims the logical port and programs the datapath,
- uses the annotation **MAC** (instead of a random one) and delivers it to the
  in-pod app via the CNI config data.

> Userspace caveat: there is no kernel netdev carrying the MAC. The in-pod DPDK
> app **must** source traffic with the Neutron-assigned MAC/IP, or
> ovn-controller's flows drop it. Use
> [verify-ovn-dpdk-binding.sh](verify-ovn-dpdk-binding.sh) to validate the
> data-plane binding before relying on it.
>
> Port lifecycle (create/delete of the logical port) is owned by Neutron/OVN,
> so no CNI-side garbage collection is needed for OVN-bound ports.

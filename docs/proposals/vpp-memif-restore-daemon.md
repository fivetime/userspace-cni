# Proposal: per-node daemon to restore VPP memifs after a VPP restart

- **Status:** Draft / Proposed
- **Project:** userspace-cni
- **Companion:** `docs/proposals/cni-del-idempotent-cleanup.md` (DEL robustness;
  this proposal is the restore half)

## Summary

When userspace-cni provisions memifs into a **VPP** that it shares with other
components (e.g. a per-node BGP/VPP operator), a restart of that VPP **loses all
the memif masters** userspace-cni created — and nothing re-creates them, because
no pod-lifecycle event fires to re-trigger the CNI. Add a small **per-node
daemon** (the existing userspace-cni DaemonSet, made long-running) that watches
its VPP connection and, on reconnect, **idempotently re-asserts the memifs it
owns**. The CNI binary stays a pure ADD/DEL plugin; this is purely additive.

## Background / problem

userspace-cni's CNI binary is **edge-triggered**: the kubelet invokes it on pod
ADD/DEL. At ADD it creates the host-side memif **master** in the node's dataplane
(for the VPP engine, imperatively over the binary API).

VPP's runtime state is **non-persistent**: a VPP process restart comes back only
with its own startup config; interfaces created at runtime by external API
clients (our memif masters) are **gone**. Since the workload pods did not change,
**no CNI ADD re-runs**, so the masters are never re-created. The pods' memif
**slaves** sit disconnected; traffic stops until the workload pods are recreated.

Observed: killing the node's VPP re-establishes the operator's own BGP (its
config is declarative and replayed) but leaves userspace-cni workloads' memifs
disconnected (100% packet loss) until those pods are recreated.

> **VPP-specific.** The OVS-DPDK engine persists its config in ovsdb, so an OVS
> restart restores ports on its own — this problem and this daemon are specific
> to the VPP engine.

## Principle / boundary

The component that **owns an interface restores it** after a dataplane restart:

- The **VPP owner** (whoever runs the VPP process) owns VPP's lifecycle/health
  and brings VPP + its *own* interfaces back (typically from a declarative
  startup config).
- **userspace-cni** owns the **pod↔VPP memif edge**, so re-asserting *its own*
  memifs once VPP is back is its job — an extension of the edge it already owns,
  not "monitoring the base network".

This deliberately rejects the alternative of having the VPP owner reconstruct
userspace-cni's memifs: that would force it to reverse-engineer userspace-cni's
annotation schema, socket-naming, shared-dir and bridge conventions — a brittle
cross-component coupling. userspace-cni restoring its own memifs needs **none of
that**: it already has the intent and the very functions that created them.

## Proposed design

A per-node daemon, shipped as the **existing userspace-cni DaemonSet** made
long-running (today it installs the CNI binary + provisions the kubeconfig, then
idles; instead it runs this daemon).

### Trigger — event-driven on the VPP connection (not polling)

- The daemon holds a persistent connection to the node VPP via govpp
  **`AsyncConnect`**, which yields a stream of `ConnectionEvent`s.
- VPP dies → the API socket drops → `Disconnected`. VPP returns (new process /
  socket) → govpp reconnects → `Connected`.
- On each `Connected`, run an **idempotent reconcile**. A transient blip (no
  state lost) reconciles to a no-op; a real restart finds the memifs gone and
  re-creates them. No need to distinguish the two.
- Being a **separate pod** (not fate-shared with VPP) makes the daemon a stable
  observer that sees the exact drop→reconnect edge — better placed than anything
  living inside the VPP pod, which would itself restart with VPP.

### Desired state — reuse userspace-cni's own logic (no new parsing)

The memifs that *should* exist on this node = the host masters for the live,
node-local pods that have a userspace network. The daemon derives them by
**reusing the functions the CNI ADD path already uses** — it is reading
userspace-cni's *own* data, not another component's:

- list node-local pods (field selector `spec.nodeName`) with the
  `k8s.v1.cni.cncf.io/networks` + `userspace/configuration-data` annotations;
- the NAD host config (`host.iftype=memif`, `host.netType`, `host.bridge`) →
  engine, mode, bridge-domain;
- the host socket path via the existing `annotations.GetPodVolumeMountHostSharedDir`
  + `getMemifSocketfileName(containerID, ifName)`.

(Equivalently, ADD could record each created memif to a node-local state file
the daemon reads; the live-pods approach is preferred as it self-corrects for
deleted pods.)

### Reconcile (idempotent)

Diff desired against the memifs currently in VPP (`memif_dump`, scoped to masters
under the userspace-cni shared dir):

- **restore-missing** — for each desired-but-absent memif, recreate the master on
  the same socket (and re-add it to its bridge-domain). The pods' slaves
  **auto-reconnect** to a master that reappears on the same socket — **no
  workload restart**. Reuses the existing `cnivpp/api` memif + bridge wrappers.
- **GC-orphan** (optional) — a master with no backing live pod (e.g. a CNI DEL
  that could not run while VPP was down) may be removed.

### Deployment

- The DaemonSet runs the daemon as its main process (a new subcommand of the
  `userspace` binary, or a small sibling binary).
- Mounts: **`/run/vpp`** (the api.sock + the shared dirs — already the hostPath
  userspace-cni uses) so it can reach VPP and the sockets.
- Identity: the daemon is a **pod**, so it uses its in-cluster ServiceAccount
  token directly (no on-host kubeconfig — that is only for the host CNI binary).
- RBAC: `pods` list/watch (node-scoped) and `network-attachment-definitions`
  get/list. Gated by a values flag (e.g. `restoreDaemon.enabled`).

## Non-goals

- The CNI binary is unchanged — still a pure ADD/DEL plugin.
- Not for the OVS-DPDK engine (ovsdb persistence already restores ports).
- The daemon does **not** manage VPP's lifecycle/health, BGP, or any non-memif
  interface — only its own memifs.
- Not a general dataplane reconciler for other components' interfaces.

## Implementation sketch

- New `cmd`/subcommand: `userspace daemon` (or `userspace-restore-daemon`).
- `AsyncConnect` → range `ConnectionEvent`; on `Connected`, call `reconcile(ctx)`.
- `reconcile`: build desired (k8s client + the reuse path above) → `memif_dump`
  for actual → diff → recreate missing via existing `cnivpp` helpers.
- Idempotency / races: creates tolerate "already exists"; coordinate memif
  socket-id allocation with a concurrent CNI ADD (reuse `CreateMemifSocket`'s
  find-or-add). DEL idempotency (companion proposal) keeps teardown clean.
- Chart: extend the userspace-cni DaemonSet (command, `/run/vpp` mount, SA +
  RBAC), behind `restoreDaemon.enabled`.

## Testing

- **Unit:** desired-state derivation from fake pods/NADs; diff (restore/GC sets);
  the connection-event handler reconciles on `Connected`.
- **Integration (VPP):** ADD two memif pods; restart VPP; assert the daemon
  re-creates the masters and the pods' ping recovers **without recreating the
  pods**; force-delete a sandbox while VPP is down → assert orphan GC.

## Open questions

- Desired source: live-pods+annotations (preferred) vs an ADD-time node-local
  state file — pick one.
- Whether to also run a slow periodic safety reconcile in addition to the
  reconnect trigger (default: reconnect-only).
- Socket-id allocation races between the daemon and concurrent CNI ADD/DEL.
- Detecting a genuinely fresh VPP instance vs a brief blip — current answer:
  don't; reconcile idempotently on every `Connected`.

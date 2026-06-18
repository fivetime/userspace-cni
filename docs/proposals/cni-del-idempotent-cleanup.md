# Proposal: make CNI DEL idempotent and always clean its own socket file

- **Status:** Draft / Proposed
- **Project:** userspace-cni
- **Companion:** bird-k8s `docs/proposals/vpp-dataplane-reconciler.md`
  (the dataplane-owner half; this proposal is only the plugin-side DEL fix)

## Summary

`CmdDel` can leak the memif socket file when the VPP-side interface is already
gone (e.g. the node VPP was restarted between ADD and DEL). Make DEL **idempotent**:
treat an already-absent memif as success, and **always** remove the socket file
the plugin created, regardless of the VPP delete outcome.

## Background / problem

DEL of a VPP memif goes through `cnivpp.DelFromHost` →
`delLocalDeviceMemif` (`cnivpp/cnivpp.go`). Today:

```go
// cnivpp/cnivpp.go : delLocalDeviceMemif (~L433)
err = vppmemif.DeleteMemifInterface(vppCh.Ch, ...InterfaceIndex(data.InterfaceSwIfIndex))
if err != nil {
    logging.Debugf(...)
    return logging.Errorf("delLocalDeviceMemif(vpp): Error deleting memif inteface: %v", err) // <-- early return
}
...
// Remove socketfile
err = configdata.FileCleanup("", memifSocketPath)   // <-- never reached when the delete above failed
return
```

If `DeleteMemifInterface` fails, the function **returns before `FileCleanup`**,
so the socket file is leaked. The same early-return shape exists in
`DelFromHost` for `RemoveBridgeInterface` (`cnivpp/cnivpp.go` ~L229).

### Observed

When the node VPP is restarted between ADD and DEL, the memif master is already
gone from VPP. DEL then fails with:

```
delLocalDeviceMemif(vpp): Error deleting memif inteface: VPPApiError: Invalid sw_if_index (-2)
cmdDel: Host ERROR - delLocalDeviceMemif(vpp): Error deleting memif inteface: ...
```

and leaves a stale socket file in the shared dir. Stale sockets accumulate; a
naive consumer that picks "the first socket" (`ls | head -1`) can then grab a
dead one.

## Principle / boundary

userspace-cni owns the pod↔VPP **edge**, including the **socket file it
creates**. Cleaning that file up is part of DEL — independent of VPP-side state.
The CNI spec expects DELETE to be **idempotent / best-effort**: an interface
that is already absent means the desired end-state (gone) is met, which is
**success**, not a hard failure that aborts cleanup.

This change keeps userspace-cni a pure CNI plugin. It does **not** add any VPP
monitoring or reconcile (that is the dataplane owner's job — see the companion
bird-k8s proposal).

## Proposed change

In `cnivpp/cnivpp.go`:

1. **`delLocalDeviceMemif`** — treat "interface not found / invalid sw_if_index"
   from `DeleteMemifInterface` as **non-fatal** (already absent ⇒ desired state
   met): log at debug and continue. **Always** run `configdata.FileCleanup` for
   the socket file (move it out from behind the success path — e.g. a `defer` or
   unconditional call), so the file is removed even when the VPP delete was a
   no-op or failed-because-absent.
2. **`DelFromHost`** — make `RemoveBridgeInterface` tolerant the same way (a
   bridge/interface already gone is not a hard error that skips the rest of DEL).
3. Ensure DEL returns success when the **end-state** (no memif, no socket) holds,
   per CNI DELETE idempotency.

Sketch:

```go
func delLocalDeviceMemif(...) (err error) {
    memifSocketPath := getMemifSocketfileName(...)
    // Always remove the socket file we created, regardless of VPP-side state.
    defer func() { _ = configdata.FileCleanup("", memifSocketPath) }()

    if e := vppmemif.DeleteMemifInterface(vppCh.Ch, ...); e != nil {
        if isAlreadyGone(e) { // invalid sw_if_index / not found
            logging.Debugf("delLocalDeviceMemif(vpp): memif already absent, treating DEL as no-op: %v", e)
            return nil
        }
        return logging.Errorf("delLocalDeviceMemif(vpp): Error deleting memif interface: %v", e)
    }
    return nil
}
```

(`isAlreadyGone` matches the VPP "invalid sw_if_index" / not-found API error.)

## Scope / non-goals

- **No** VPP monitoring / reconcile added to userspace-cni — orphan memifs left
  when DEL never runs (node crash, force-delete) are out of scope here and are
  handled by the dataplane reconciler (bird-k8s proposal).
- **No** change to ADD behavior.
- Does not change the socket-path / shared-dir conventions.

## Testing

- **Unit:** `delLocalDeviceMemif` with `DeleteMemifInterface` returning an
  invalid-sw_if_index error ⇒ no error returned, `FileCleanup` still invoked
  (assert via a fake VPP channel + temp socket file).
- **Integration:** ADD; restart VPP; DEL ⇒ succeeds, socket file removed, no
  residue in the shared dir.

## Possible follow-up (separate)

If the bird-k8s reconciler needs more than the current annotation carries to
recreate a memif master (socket path, bridge-domain, role, mode), enrich the
config userspace-cni persists to the pod annotation. Tracked with the bird-k8s
proposal; not required for this DEL fix.

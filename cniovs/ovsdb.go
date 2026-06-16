// Copyright 2018-2020 Red Hat, Intel Corp.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// OVS control surface for the OVS-DPDK engine.
//
// This file replaces the previous ovsctrl.go which shelled out to ovs-vsctl
// and parsed its text output. All operations now go directly to the local
// ovsdb-server over its unix socket via libovsdb — no exec, no text parsing,
// no PATH lookups. The package-level singleton client is connected lazily
// on first use and reused for the rest of the CNI invocation (CNI processes
// are short-lived, so we don't bother with cleanup).
//
// Function names match the previous ovs-vsctl wrappers so cniovs.go callers
// stay unchanged.

package cniovs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ovn-org/libovsdb/client"
	"github.com/ovn-org/libovsdb/model"
	"github.com/ovn-org/libovsdb/ovsdb"

	"github.com/intel/userspace-cni-network-plugin/logging"
)

const (
	defaultOVSSocket = "unix:/var/run/openvswitch/db.sock"
	// Where ovs-vswitchd creates dpdkvhostuser server sockets by default. We
	// move them into the user-requested sharedDir after creation.
	defaultOVSSocketDir = "/usr/local/var/run/openvswitch/"

	tblOpenvSwitch = "Open_vSwitch"
	tblBridge      = "Bridge"
	tblPort        = "Port"
	tblInterface   = "Interface"

	// ovsdbTimeout bounds every connect and transact. Without it a hung
	// ovsdb-server would block the whole (short-lived) CNI invocation
	// indefinitely.
	ovsdbTimeout = 10 * time.Second

	// ovsCNIOwner tags every port/interface this plugin creates via
	// external_ids, so orphaned ports left by a crashed CNI run can be
	// identified and reclaimed. Mirrors the ownership-tagging pattern used
	// by kubevirt/ovs-cni.
	ovsCNIOwner = "userspace-cni"
)

// zeroUUID matches all rows on `_uuid != zeroUUID`. This is the standard
// OVSDB idiom for mutating the singleton Open_vSwitch row without knowing
// its UUID in advance.
var zeroUUID = ovsdb.UUID{GoUUID: "00000000-0000-0000-0000-000000000000"}

// Minimal client DB model — libovsdb requires at least one model entry per
// table we want to read row UUIDs back from. We only ever read `_uuid`, so
// these structs deliberately carry no other fields.
type rowOpenvSwitch struct {
	UUID string `ovsdb:"_uuid"`
}
type rowBridge struct {
	UUID string `ovsdb:"_uuid"`
}
type rowPort struct {
	UUID string `ovsdb:"_uuid"`
}
type rowInterface struct {
	UUID string `ovsdb:"_uuid"`
}

var (
	ovsClient     client.Client
	ovsClientOnce sync.Once
	ovsClientErr  error
)

// getOvsClient returns a singleton OVSDB client connected to the local
// ovsdb-server. The client is initialized lazily on first call.
func getOvsClient() (client.Client, error) {
	ovsClientOnce.Do(func() {
		dbModel, err := model.NewClientDBModel("Open_vSwitch", map[string]model.Model{
			tblOpenvSwitch: &rowOpenvSwitch{},
			tblBridge:      &rowBridge{},
			tblPort:        &rowPort{},
			tblInterface:   &rowInterface{},
		})
		if err != nil {
			ovsClientErr = fmt.Errorf("create OVSDB client model: %w", err)
			return
		}
		c, err := client.NewOVSDBClient(dbModel, client.WithEndpoint(defaultOVSSocket))
		if err != nil {
			ovsClientErr = fmt.Errorf("create OVSDB client: %w", err)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), ovsdbTimeout)
		defer cancel()
		if err := c.Connect(ctx); err != nil {
			ovsClientErr = fmt.Errorf("connect to OVSDB at %s: %w", defaultOVSSocket, err)
			return
		}
		ovsClient = c
	})
	return ovsClient, ovsClientErr
}

// transact runs a sequence of OVSDB operations atomically and surfaces any
// per-op error as a Go error.
func transact(ops []ovsdb.Operation) ([]ovsdb.OperationResult, error) {
	c, err := getOvsClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), ovsdbTimeout)
	defer cancel()
	reply, txErr := c.Transact(ctx, ops...)
	if txErr != nil {
		return nil, fmt.Errorf("OVSDB transact: %w", txErr)
	}
	if len(reply) < len(ops) {
		return nil, errors.New("OVSDB transact: fewer replies than ops")
	}
	for i, o := range reply {
		if o.Error != "" {
			return nil, fmt.Errorf("OVSDB op %d: %s — %s", i, o.Error, o.Details)
		}
	}
	return reply, nil
}

// selectRows runs a `select` and returns matching rows (zero or more).
func selectRows(table string, cond ovsdb.Condition, columns []string) ([]ovsdb.Row, error) {
	op := ovsdb.Operation{
		Op:      "select",
		Table:   table,
		Where:   []ovsdb.Condition{cond},
		Columns: columns,
	}
	reply, err := transact([]ovsdb.Operation{op})
	if err != nil {
		return nil, err
	}
	return reply[0].Rows, nil
}

// ----- Bridge operations -----------------------------------------------------

// createBridge inserts a Bridge row with datapath_type=netdev (OVS-DPDK) and
// attaches it to the singleton Open_vSwitch row. datapath_type=netdev is
// non-negotiable for this engine — dpdkvhostuser ports only attach to a
// userspace datapath bridge.
func createBridge(bridgeName string) error {
	bridgeUUIDName := "newbridge"

	bridgeRow := ovsdb.Row{
		"name":          bridgeName,
		"datapath_type": "netdev",
	}
	bridgeOp := ovsdb.Operation{
		Op:       "insert",
		Table:    tblBridge,
		Row:      bridgeRow,
		UUIDName: bridgeUUIDName,
	}

	bridgesSet, err := ovsdb.NewOvsSet(ovsdb.UUID{GoUUID: bridgeUUIDName})
	if err != nil {
		return fmt.Errorf("createBridge: build bridges set: %w", err)
	}
	mutation := ovsdb.NewMutation("bridges", ovsdb.MutateOperationInsert, bridgesSet)
	mutateOp := ovsdb.Operation{
		Op:        "mutate",
		Table:     tblOpenvSwitch,
		Mutations: []ovsdb.Mutation{*mutation},
		Where:     []ovsdb.Condition{ovsdb.NewCondition("_uuid", ovsdb.ConditionNotEqual, zeroUUID)},
	}

	_, err = transact([]ovsdb.Operation{bridgeOp, mutateOp})
	if err != nil {
		return fmt.Errorf("createBridge %q: %w", bridgeName, err)
	}
	logging.Verbosef("ovsdb.createBridge: bridge %q created (datapath_type=netdev)", bridgeName)
	return nil
}

// deleteBridge removes the named Bridge row and detaches it from the
// Open_vSwitch.bridges set. Returns an error if the bridge does not exist.
func deleteBridge(bridgeName string) error {
	uuid, err := bridgeUUID(bridgeName)
	if err != nil {
		return err
	}

	delOp := ovsdb.Operation{
		Op:    "delete",
		Table: tblBridge,
		Where: []ovsdb.Condition{ovsdb.NewCondition("name", ovsdb.ConditionEqual, bridgeName)},
	}

	bridgesSet, err := ovsdb.NewOvsSet(uuid)
	if err != nil {
		return fmt.Errorf("deleteBridge: build bridges set: %w", err)
	}
	mutation := ovsdb.NewMutation("bridges", ovsdb.MutateOperationDelete, bridgesSet)
	mutateOp := ovsdb.Operation{
		Op:        "mutate",
		Table:     tblOpenvSwitch,
		Mutations: []ovsdb.Mutation{*mutation},
		Where:     []ovsdb.Condition{ovsdb.NewCondition("_uuid", ovsdb.ConditionNotEqual, zeroUUID)},
	}

	_, err = transact([]ovsdb.Operation{delOp, mutateOp})
	if err != nil {
		return fmt.Errorf("deleteBridge %q: %w", bridgeName, err)
	}
	logging.Verbosef("ovsdb.deleteBridge: bridge %q deleted", bridgeName)
	return nil
}

// findBridge returns true if a Bridge row with the given name exists.
func findBridge(bridgeName string) bool {
	rows, err := selectRows(tblBridge,
		ovsdb.NewCondition("name", ovsdb.ConditionEqual, bridgeName),
		[]string{"name"})
	if err != nil {
		logging.Errorf("ovsdb.findBridge: %v", err)
		return false
	}
	return len(rows) > 0
}

// doesBridgeContainInterfaces returns true if the named bridge has at least
// one port row attached.
func doesBridgeContainInterfaces(bridgeName string) bool {
	rows, err := selectRows(tblBridge,
		ovsdb.NewCondition("name", ovsdb.ConditionEqual, bridgeName),
		[]string{"ports"})
	if err != nil || len(rows) == 0 {
		return false
	}
	return portsSetNonEmpty(rows[0])
}

// portsSetNonEmpty reports whether a Bridge row's "ports" column holds at
// least one port UUID. Pure decoding of libovsdb's set representation, split
// out so it can be unit-tested without a live ovsdb-server.
func portsSetNonEmpty(row ovsdb.Row) bool {
	ports, ok := row["ports"]
	if !ok {
		return false
	}
	// "ports" is an OvsSet of UUIDs; an empty bridge has zero entries.
	if set, ok := ports.(ovsdb.OvsSet); ok {
		return len(set.GoSet) > 0
	}
	// libovsdb collapses single-element sets to a bare UUID — non-empty.
	if _, ok := ports.(ovsdb.UUID); ok {
		return true
	}
	return false
}

// bridgeUUID looks up the UUID of a Bridge row by name.
func bridgeUUID(bridgeName string) (ovsdb.UUID, error) {
	rows, err := selectRows(tblBridge,
		ovsdb.NewCondition("name", ovsdb.ConditionEqual, bridgeName),
		[]string{"_uuid"})
	if err != nil {
		return ovsdb.UUID{}, err
	}
	if len(rows) == 0 {
		return ovsdb.UUID{}, fmt.Errorf("bridge %q not found", bridgeName)
	}
	u, ok := rows[0]["_uuid"].(ovsdb.UUID)
	if !ok {
		return ovsdb.UUID{}, fmt.Errorf("bridge %q: _uuid has unexpected type %T", bridgeName, rows[0]["_uuid"])
	}
	return u, nil
}

// ----- Port operations -------------------------------------------------------

// vhostPortConfig carries the optional per-port settings for createVhostPort.
// Zero values mean "leave the OVS default".
type vhostPortConfig struct {
	containerID  string // external_ids:container-id (ownership)
	ifName       string // external_ids:interface (ownership)
	ovnPort      string // external_ids:iface-id (OVN binding)
	mtu          int    // mtu_request
	vlanID       int    // access tag / native VLAN
	trunks       []int  // trunk VLANs
	vlanMode     string // explicit vlan_mode (access|trunk|native-*|dot1q-tunnel)
	ingressRate  int    // ingress_policing_rate, kbps
	ingressBurst int    // ingress_policing_burst, kb
}

// createVhostPort inserts an Interface + Port pair and attaches the port to
// the named bridge. When client=true the port type is dpdkvhostuserclient
// and options:vhost-server-path is set to <sockDir>/<sockName>, meaning
// OVS will connect outward to the socket created by the in-container DPDK
// app. When client=false the port type is dpdkvhostuser (OVS as server)
// and ovs-vswitchd creates the socket under defaultOVSSocketDir; we move
// it into sockDir so it's reachable inside the pod.
//
// cfg carries the optional per-port settings (ownership tags, OVN iface-id,
// MTU, VLAN, QoS). See vhostPortConfig. Returns the interface/port name (same
// string as sockName).
func createVhostPort(sockDir, sockName string, vhostClient bool, bridgeName string, cfg vhostPortConfig) (string, error) {
	intfUUIDName := "newintf"
	portUUIDName := "newport"

	intfType := "dpdkvhostuser"
	if vhostClient {
		intfType = "dpdkvhostuserclient"
	}

	extIDsMap := map[string]string{
		"owner":        ovsCNIOwner,
		"container-id": cfg.containerID,
		"interface":    cfg.ifName,
	}
	if cfg.ovnPort != "" {
		// The glue for OVN: ovn-controller claims the logical port whose UUID
		// matches iface-id and programs the datapath for this port.
		extIDsMap["iface-id"] = cfg.ovnPort
	}
	extIDs, err := ovsdb.NewOvsMap(extIDsMap)
	if err != nil {
		return "", fmt.Errorf("createVhostPort: build external_ids map: %w", err)
	}
	intfRow := ovsdb.Row{
		"name":         sockName,
		"type":         intfType,
		"external_ids": extIDs,
	}
	if cfg.mtu > 0 {
		intfRow["mtu_request"] = cfg.mtu
	}
	if cfg.ingressRate > 0 {
		// Per-port ingress rate limiting (QoS). Burst defaults sanely in OVS
		// when unset, so only emit it when the caller asked for one.
		intfRow["ingress_policing_rate"] = cfg.ingressRate
		if cfg.ingressBurst > 0 {
			intfRow["ingress_policing_burst"] = cfg.ingressBurst
		}
	}
	if vhostClient {
		opts, err := ovsdb.NewOvsMap(map[string]string{
			"vhost-server-path": filepath.Join(sockDir, sockName),
		})
		if err != nil {
			return "", fmt.Errorf("createVhostPort: build options map: %w", err)
		}
		intfRow["options"] = opts
	}
	intfOp := ovsdb.Operation{
		Op:       "insert",
		Table:    tblInterface,
		Row:      intfRow,
		UUIDName: intfUUIDName,
	}

	intfSet, err := ovsdb.NewOvsSet(ovsdb.UUID{GoUUID: intfUUIDName})
	if err != nil {
		return "", fmt.Errorf("createVhostPort: build interfaces set: %w", err)
	}
	portRow := ovsdb.Row{
		"name":       sockName,
		"interfaces": intfSet,
	}
	// VLAN: an explicit vlan_mode wins; otherwise OVS infers access (tag) or
	// trunk (trunks). tag doubles as the native VLAN for native-* modes.
	if cfg.vlanMode != "" {
		portRow["vlan_mode"] = cfg.vlanMode
	}
	if cfg.vlanID > 0 {
		portRow["tag"] = cfg.vlanID
	}
	if len(cfg.trunks) > 0 {
		trunkSet, err := ovsdb.NewOvsSet(cfg.trunks)
		if err != nil {
			return "", fmt.Errorf("createVhostPort: build trunks set: %w", err)
		}
		portRow["trunks"] = trunkSet
	}
	portOp := ovsdb.Operation{
		Op:       "insert",
		Table:    tblPort,
		Row:      portRow,
		UUIDName: portUUIDName,
	}

	portSet, err := ovsdb.NewOvsSet(ovsdb.UUID{GoUUID: portUUIDName})
	if err != nil {
		return "", fmt.Errorf("createVhostPort: build ports set: %w", err)
	}
	mutation := ovsdb.NewMutation("ports", ovsdb.MutateOperationInsert, portSet)
	mutateOp := ovsdb.Operation{
		Op:        "mutate",
		Table:     tblBridge,
		Mutations: []ovsdb.Mutation{*mutation},
		Where:     []ovsdb.Condition{ovsdb.NewCondition("name", ovsdb.ConditionEqual, bridgeName)},
	}

	if _, err = transact([]ovsdb.Operation{intfOp, portOp, mutateOp}); err != nil {
		return "", fmt.Errorf("createVhostPort %q on bridge %q: %w", sockName, bridgeName, err)
	}

	// In server mode ovs-vswitchd creates the socket under its built-in
	// directory. Move it into the caller-requested sharedDir so the pod
	// (which has sharedDir mounted) can see it. In client mode OVS connects
	// outward to a socket the in-pod DPDK app creates, so nothing to move.
	if !vhostClient {
		src := filepath.Join(defaultOVSSocketDir, sockName)
		dst := filepath.Join(sockDir, sockName)
		if err := os.Rename(src, dst); err != nil {
			// Non-fatal: log and continue. The original ovs-vsctl path did
			// the same — failing here would leave a stranded OVS port that
			// the caller's defer cleanup can pick up later.
			logging.Errorf("ovsdb.createVhostPort: rename %s → %s: %v", src, dst, err)
		}
	}

	logging.Verbosef("ovsdb.createVhostPort: %s (%s) attached to %s", sockName, intfType, bridgeName)
	return sockName, nil
}

// deleteVhostPort removes the named Port + Interface and detaches the port
// from the bridge.
func deleteVhostPort(sockName, bridgeName string) error {
	uuid, err := portUUID(sockName)
	if err != nil {
		// Idempotent on missing — matches the prior `--if-exists del-port`.
		logging.Verbosef("ovsdb.deleteVhostPort: %s already gone: %v", sockName, err)
		return nil
	}

	delIntfOp := ovsdb.Operation{
		Op:    "delete",
		Table: tblInterface,
		Where: []ovsdb.Condition{ovsdb.NewCondition("name", ovsdb.ConditionEqual, sockName)},
	}
	delPortOp := ovsdb.Operation{
		Op:    "delete",
		Table: tblPort,
		Where: []ovsdb.Condition{ovsdb.NewCondition("name", ovsdb.ConditionEqual, sockName)},
	}

	portSet, err := ovsdb.NewOvsSet(uuid)
	if err != nil {
		return fmt.Errorf("deleteVhostPort: build ports set: %w", err)
	}
	mutation := ovsdb.NewMutation("ports", ovsdb.MutateOperationDelete, portSet)
	mutateOp := ovsdb.Operation{
		Op:        "mutate",
		Table:     tblBridge,
		Mutations: []ovsdb.Mutation{*mutation},
		Where:     []ovsdb.Condition{ovsdb.NewCondition("name", ovsdb.ConditionEqual, bridgeName)},
	}

	if _, err = transact([]ovsdb.Operation{delIntfOp, delPortOp, mutateOp}); err != nil {
		return fmt.Errorf("deleteVhostPort %q on bridge %q: %w", sockName, bridgeName, err)
	}
	logging.Verbosef("ovsdb.deleteVhostPort: %s detached from %s", sockName, bridgeName)
	return nil
}

// portUUID looks up the UUID of a Port row by name.
func portUUID(portName string) (ovsdb.UUID, error) {
	rows, err := selectRows(tblPort,
		ovsdb.NewCondition("name", ovsdb.ConditionEqual, portName),
		[]string{"_uuid"})
	if err != nil {
		return ovsdb.UUID{}, err
	}
	if len(rows) == 0 {
		return ovsdb.UUID{}, fmt.Errorf("port %q not found", portName)
	}
	u, ok := rows[0]["_uuid"].(ovsdb.UUID)
	if !ok {
		return ovsdb.UUID{}, fmt.Errorf("port %q: _uuid has unexpected type %T", portName, rows[0]["_uuid"])
	}
	return u, nil
}

// getVhostPortMac reads back the MAC OVS assigned to the port (after the
// dpdkvhostuser interface negotiates one). Returns "" with no error if the
// MAC is not yet populated — caller decides whether to retry or move on.
func getVhostPortMac(sockName string) (string, error) {
	rows, err := selectRows(tblPort,
		ovsdb.NewCondition("name", ovsdb.ConditionEqual, sockName),
		[]string{"mac"})
	if err != nil {
		return "", fmt.Errorf("getVhostPortMac %q: %w", sockName, err)
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("port %q not found", sockName)
	}
	macVal, ok := rows[0]["mac"]
	if !ok {
		return "", nil
	}
	return macFromValue(macVal), nil
}

// macFromValue decodes an optional OVSDB "mac" column into a plain string.
// An unset optional column comes back as an empty OvsSet; a present one as a
// bare string (libovsdb collapses the single-element set). Split out so it can
// be unit-tested without a live ovsdb-server.
func macFromValue(macVal interface{}) string {
	switch v := macVal.(type) {
	case string:
		return v
	case ovsdb.OvsSet:
		if len(v.GoSet) == 0 {
			return ""
		}
		if s, ok := v.GoSet[0].(string); ok {
			return s
		}
		return ""
	default:
		return ""
	}
}

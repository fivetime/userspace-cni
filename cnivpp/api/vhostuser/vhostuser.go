// Copyright (c) 2017 Cisco and/or its affiliates.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at:
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Binary simple-client is an example VPP management application that exercises the
// govpp API on real-world use-cases.
package vppvhostuser

import (
	"fmt"

	"go.fd.io/govpp/api"
	"go.fd.io/govpp/binapi/ethernet_types"
	"go.fd.io/govpp/binapi/interface_types"
	"go.fd.io/govpp/binapi/vhost_user"
)

//
// Constants
//

const debugVhost = false

type VhostUserMode bool

const (
	ModeClient VhostUserMode = false
	ModeServer VhostUserMode = true
)

//
// API Functions
//

// CreateParams holds the inputs for CreateVhostUserInterface.
type CreateParams struct {
	IsServer       bool   // true: VPP listens (server); false: VPP connects (client)
	SockFilename   string // directory and filename of the socket file
	EnableGso      bool
	EnablePacked   bool
	EnableEventIdx bool
	MAC            string // optional fixed MAC (aa:bb:cc:dd:ee:ff); "" -> VPP picks one
}

// Attempt to create a Vhost-User Interface via create_vhost_user_if_v2
// (create_vhost_user_if is deprecated).
func CreateVhostUserInterface(ch api.Channel, p CreateParams) (swIfIndex interface_types.InterfaceIndex, err error) {

	// Populate the Add Structure
	req := &vhost_user.CreateVhostUserIfV2{
		IsServer:          p.IsServer,
		SockFilename:      p.SockFilename,
		Renumber:          false,
		EnableGso:         p.EnableGso,
		EnablePacked:      p.EnablePacked,
		EnableEventIdx:    p.EnableEventIdx,
		CustomDevInstance: 0,
		//Tag: "",
	}
	if p.MAC != "" {
		hw, perr := ethernet_types.ParseMacAddress(p.MAC)
		if perr != nil {
			return 0, fmt.Errorf("vhostuser: invalid MAC %q: %w", p.MAC, perr)
		}
		req.UseCustomMac = true
		req.MacAddress = hw
	}

	reply := &vhost_user.CreateVhostUserIfV2Reply{}

	err = ch.SendRequest(req).ReceiveReply(reply)

	if err != nil {
		if debugVhost {
			fmt.Println("Error creating vhostUser interface:", err)
		}
		return
	} else {
		swIfIndex = reply.SwIfIndex
	}

	return
}

// Attempt to delete a Vhost-User interface.
func DeleteVhostUserInterface(ch api.Channel, swIfIndex interface_types.InterfaceIndex) (err error) {

	// Populate the Delete Structure
	req := &vhost_user.DeleteVhostUserIf{
		SwIfIndex: swIfIndex,
	}

	reply := &vhost_user.DeleteVhostUserIfReply{}

	err = ch.SendRequest(req).ReceiveReply(reply)

	if err != nil {
		if debugVhost {
			fmt.Println("Error deleting vhostUser interface:", err)
		}
		return err
	}

	return err
}

// Dump the set of existing Vhost-User interfaces to stdout.
func DumpVhostUser(ch api.Channel) {
	var count int

	// Populate the Message Structure
	req := &vhost_user.SwInterfaceVhostUserDump{}
	reqCtx := ch.SendMultiRequest(req)

	fmt.Printf("Vhost-User Interface List:\n")
	for {
		reply := &vhost_user.SwInterfaceVhostUserDetails{}
		stop, err := reqCtx.ReceiveReply(reply)
		if stop {
			break // break out of the loop
		}
		if err != nil {
			fmt.Println("Error dumping vhostUser interface:", err)
		}
		//fmt.Printf("%+v\n", reply)

		fmt.Printf("    SwIfId=%d Mode=%t IfName=%s NumReg=%d SockErrno=%d FeaturesFirst32=%d FeaturesLast32=%d HdrSz=%d SockFile=%s\n",
			reply.SwIfIndex,
			reply.IsServer,
			string(reply.InterfaceName),
			reply.NumRegions,
			reply.SockErrno,
			reply.FeaturesFirst32,
			reply.FeaturesLast32,
			reply.VirtioNetHdrSz,
			string(reply.SockFilename))

		count++
	}

	fmt.Printf("  Interface Count: %d\n", count)
}

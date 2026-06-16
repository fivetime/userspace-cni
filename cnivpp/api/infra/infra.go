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
package vppinfra

import (
	"fmt"
	"time"

	"github.com/sirupsen/logrus"

	"go.fd.io/govpp"
	"go.fd.io/govpp/api"
	"go.fd.io/govpp/core"
)

// Constants
const debugInfra = false

// replyTimeout bounds how long a single request waits for a reply from VPP.
// Without it a hung or unresponsive vpp would block the whole (short-lived)
// CNI invocation indefinitely. govpp's per-channel timeout is the idiomatic
// knob for this — no need to wrap every call in our own context.
const replyTimeout = 5 * time.Second

// Types
type ConnectionData struct {
	conn           *core.Connection
	disconnectFlag bool
	Ch             api.Channel
	closeFlag      bool
}

//
// API Functions
//

// Open a Connection and Channel to VPP to allow communication to VPP.
func VppOpenCh() (ConnectionData, error) {

	var vppCh ConnectionData
	var err error

	// Set log level
	//   Logrus has six logging levels: DebugLevel, InfoLevel, WarningLevel, ErrorLevel, FatalLevel and PanicLevel.
	core.SetLogger(&logrus.Logger{Level: logrus.ErrorLevel})

	// Connect to VPP
	vppCh.conn, err = govpp.Connect("")
	if err != nil {
		if debugInfra {
			fmt.Println("Error:", err)
		}
		return vppCh, err
	}
	vppCh.disconnectFlag = true

	// Create an API channel to VPP
	vppCh.Ch, err = vppCh.conn.NewAPIChannel()
	if err != nil {
		VppCloseCh(vppCh)
		if debugInfra {
			fmt.Println("Error:", err)
		}
		return vppCh, err
	}
	vppCh.Ch.SetReplyTimeout(replyTimeout)
	vppCh.closeFlag = true

	return vppCh, err
}

// Close the Connection and Channel to VPP.
func VppCloseCh(vppCh ConnectionData) {

	if vppCh.closeFlag {
		vppCh.Ch.Close()
		vppCh.closeFlag = false
	}

	if vppCh.disconnectFlag {
		vppCh.conn.Disconnect()
		vppCh.disconnectFlag = false
	}
}

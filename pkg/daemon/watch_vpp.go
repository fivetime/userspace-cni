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

package daemon

import (
	"context"
	"fmt"
	"time"

	"go.fd.io/govpp"
	"go.fd.io/govpp/core"

	"github.com/intel/userspace-cni-network-plugin/logging"
)

const (
	// reconnectAttempts is intentionally large so govpp keeps retrying within a
	// single connect cycle while VPP is down (a VPP/pod restart can take many
	// seconds); the outer Run loop re-connects if a cycle ever ends.
	reconnectAttempts = 1 << 30
	reconnectInterval = 500 * time.Millisecond
	syncReplyTimeout  = 5 * time.Second
	runRetryBackoff   = 2 * time.Second
	// reconcileDebounce coalesces a burst of (re)connect events into one reconcile.
	reconcileDebounce = time.Second
)

// Run watches the node VPP connection and re-asserts this node's memif masters
// on every (re)connect, until ctx is cancelled. apiSocket is the VPP binary-API
// socket ("" → govpp default /run/vpp/api.sock); socketPrefix scopes which
// masters are managed (the userspace-cni shared dir).
func (r *Reconciler) Run(ctx context.Context, apiSocket, socketPrefix string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.watchOnce(ctx, apiSocket, socketPrefix); err != nil {
			logging.Warningf("restore-daemon: VPP watch cycle ended: %v; reconnecting", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(runRetryBackoff):
		}
	}
}

// watchOnce holds one VPP connection and reconciles on each Connected event. It
// returns when the connection fails for good or the event stream closes (the
// caller re-connects), or nil when ctx is cancelled.
func (r *Reconciler) watchOnce(ctx context.Context, apiSocket, socketPrefix string) error {
	conn, events, err := govpp.AsyncConnect(apiSocket, reconnectAttempts, reconnectInterval)
	if err != nil {
		return fmt.Errorf("vpp async connect: %w", err)
	}
	defer conn.Disconnect()

	// Coalesce a burst of (re)connect events (VPP can flap while it settles) into
	// a single reconcile after a short quiet period. go1.23+ Timer.Reset/Stop
	// drain the channel, so no manual drain is needed.
	debounce := time.NewTimer(time.Hour)
	debounce.Stop()
	defer debounce.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-events:
			if !ok {
				return fmt.Errorf("vpp event stream closed")
			}
			switch ev.State {
			case core.Connected:
				logging.Infof("restore-daemon: VPP connected; reconcile in %s", reconcileDebounce)
				if r.Status != nil {
					r.Status.SetConnected(true)
				}
				debounce.Reset(reconcileDebounce)
			case core.Disconnected:
				logging.Infof("restore-daemon: VPP disconnected; will re-assert memifs on reconnect")
				if r.Status != nil {
					r.Status.SetConnected(false)
				}
				debounce.Stop()
			case core.Failed:
				if r.Status != nil {
					r.Status.SetConnected(false)
				}
				return fmt.Errorf("vpp connection failed: %v", ev.Error)
			}
		case <-debounce.C:
			r.onConnected(ctx, conn, socketPrefix)
		}
	}
}

// onConnected opens a fresh API channel on the (re)connected VPP, points the
// Reconciler's dataplane at it, and runs one idempotent Sync.
func (r *Reconciler) onConnected(ctx context.Context, conn *core.Connection, socketPrefix string) {
	ch, err := conn.NewAPIChannel()
	if err != nil {
		logging.Errorf("restore-daemon: open API channel: %v", err)
		return
	}
	defer ch.Close()
	ch.SetReplyTimeout(syncReplyTimeout)

	r.DP = NewVPPDataplane(ch, socketPrefix)
	created, deleted, err := r.Sync(ctx)
	if r.Status != nil {
		r.Status.RecordReconcile(created, deleted, err)
	}
	if err != nil {
		logging.Errorf("restore-daemon: memif reconcile failed: %v", err)
		return
	}
	if created > 0 || deleted > 0 {
		logging.Infof("restore-daemon: memif reconcile done: %d created, %d deleted", created, deleted)
	} else {
		logging.Verbosef("restore-daemon: memif reconcile: no changes")
	}
}

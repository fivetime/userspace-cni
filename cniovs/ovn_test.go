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

package cniovs

import (
	"encoding/json"
	"testing"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/stretchr/testify/assert"

	"github.com/intel/userspace-cni-network-plugin/pkg/types"
)

func TestResolveOvnArgs(t *testing.T) {
	testCases := []struct {
		name     string
		envArgs  string
		cni      map[string]json.RawMessage
		wantPort string
		wantMAC  string
	}{
		{
			name:     "from CNI_ARGS env",
			envArgs:  "IgnoreUnknown=true;OvnPort=port-1;MAC=fa:16:3e:00:00:01",
			wantPort: "port-1",
			wantMAC:  "fa:16:3e:00:00:01",
		},
		{
			name:     "from stdin args.cni",
			cni:      map[string]json.RawMessage{"OvnPort": json.RawMessage(`"port-2"`), "mac": json.RawMessage(`"fa:16:3e:00:00:02"`)},
			wantPort: "port-2",
			wantMAC:  "fa:16:3e:00:00:02",
		},
		{
			name:     "case-insensitive key",
			cni:      map[string]json.RawMessage{"ovnport": json.RawMessage(`"port-3"`)},
			wantPort: "port-3",
		},
		{
			name: "non-string value skipped",
			cni:  map[string]json.RawMessage{"OvnPort": json.RawMessage(`123`)},
		},
		{
			name:     "env wins over stdin",
			envArgs:  "IgnoreUnknown=true;OvnPort=env-port",
			cni:      map[string]json.RawMessage{"OvnPort": json.RawMessage(`"stdin-port"`)},
			wantPort: "env-port",
		},
		{
			name: "nothing supplied",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			conf := &types.NetConf{}
			if tc.cni != nil {
				conf.Args = &types.PluginArgs{Cni: tc.cni}
			}
			args := &skel.CmdArgs{Args: tc.envArgs}

			gotPort, gotMAC := resolveOvnArgs(conf, args)
			assert.Equal(t, tc.wantPort, gotPort, "ovnPort")
			assert.Equal(t, tc.wantMAC, gotMAC, "mac")
		})
	}
}

// Command mock-cilium-agent runs the REAL Cilium agent control plane with the
// FAKE datapath cell, against a real kube-apiserver.
//
// Instead of re-implementing cilium-agent's control-plane logic, this vendors
// the actual Cilium source in-tree and composes its `Infrastructure +
// ControlPlane` cells with the upstream-provided `pkg/datapath/fake.Cell`
// (the same cell Cilium's own controlplane test suite uses).
//
// IMPORTANT (verified by hands-on testing, see ../../cmd/mock-cilium-agent/README.md
// "Verified capabilities & limitations"): despite the name, this is NOT
// BPF-free. `option.Config.DryMode` skips attaching/loading BPF *programs*
// and building the real veth/route/iptables dataplane, but several cells
// outside fake-datapath.Cell (pkg/maps/metricsmap, the loadbalancer
// reconciler's lbmap, ratelimitmap) unconditionally create real BPF *maps*
// via bpf(2), and daemon_main.go unconditionally mounts/verifies the bpffs at
// startup. The container therefore still needs CAP_BPF/CAP_SYS_ADMIN/
// CAP_SYS_RESOURCE and a bpffs mount (see cmd/mock-cilium-agent/README.md).
package main

import (
	statedbReconciler "github.com/cilium/statedb/reconciler"

	"github.com/cilium/statedb"

	"github.com/cilium/cilium/api/v1/server"
	daemonapi "github.com/cilium/cilium/api/v1/server/restapi/daemon"
	prefilterapi "github.com/cilium/cilium/api/v1/server/restapi/prefilter"
	"github.com/cilium/cilium/daemon/cmd"
	fakeDatapath "github.com/cilium/cilium/pkg/datapath/fake"
	bandwidthTypes "github.com/cilium/cilium/pkg/datapath/linux/bandwidth/types"
	"github.com/cilium/cilium/pkg/datapath/linux/route/reconciler"
	"github.com/cilium/cilium/pkg/datapath/neighbor"
	"github.com/cilium/cilium/pkg/hive"
	"github.com/cilium/cilium/pkg/maps/ctmap"
	"github.com/cilium/cilium/pkg/maps/policymap"
	"github.com/cilium/cilium/pkg/maps/subnet"
	monitorAgent "github.com/cilium/cilium/pkg/monitor/agent"
	"github.com/cilium/cilium/pkg/option"
	"github.com/cilium/hive/cell"
	"github.com/go-openapi/runtime/middleware"
)

// mockAPIHandlers provides nil-stub implementations of the REST API handlers
// that real Cilium's datapath cell would provide (prefilter, map, node-id).
// Since DryMode is set and *server.Server's API is never actually served, these
// just need to satisfy the hive type graph — they're never invoked.
type mockAPIHandlersOut struct {
	cell.Out

	GetMapHandler           daemonapi.GetMapHandler
	GetMapNameHandler       daemonapi.GetMapNameHandler
	GetMapNameEventsHandler daemonapi.GetMapNameEventsHandler
	GetNodeIdsHandler       daemonapi.GetNodeIdsHandler
	GetPrefilterHandler     prefilterapi.GetPrefilterHandler
	PatchPrefilterHandler   prefilterapi.PatchPrefilterHandler
	DeletePrefilterHandler  prefilterapi.DeletePrefilterHandler
}

func newMockAPIHandlers() mockAPIHandlersOut {
	ni := func(op string) middleware.Responder {
		return middleware.NotImplemented(op + " is not implemented in mock-cilium-agent")
	}
	return mockAPIHandlersOut{
		GetMapHandler:           daemonapi.GetMapHandlerFunc(func(daemonapi.GetMapParams) middleware.Responder { return ni("GetMap") }),
		GetMapNameHandler:       daemonapi.GetMapNameHandlerFunc(func(daemonapi.GetMapNameParams) middleware.Responder { return ni("GetMapName") }),
		GetMapNameEventsHandler: daemonapi.GetMapNameEventsHandlerFunc(func(daemonapi.GetMapNameEventsParams) middleware.Responder { return ni("GetMapNameEvents") }),
		GetNodeIdsHandler:       daemonapi.GetNodeIdsHandlerFunc(func(daemonapi.GetNodeIdsParams) middleware.Responder { return ni("GetNodeIds") }),
		GetPrefilterHandler:     prefilterapi.GetPrefilterHandlerFunc(func(prefilterapi.GetPrefilterParams) middleware.Responder { return ni("GetPrefilter") }),
		PatchPrefilterHandler:   prefilterapi.PatchPrefilterHandlerFunc(func(prefilterapi.PatchPrefilterParams) middleware.Responder { return ni("PatchPrefilter") }),
		DeletePrefilterHandler:  prefilterapi.DeletePrefilterHandlerFunc(func(prefilterapi.DeletePrefilterParams) middleware.Responder { return ni("DeletePrefilter") }),
	}
}

// MockAgent re-wires Cilium's daemon to use the fake datapath cell.
var MockAgent = cell.Module(
	"mock-agent",
	"Cilium Agent datapath-mocked",

	cmd.Infrastructure,
	cmd.ControlPlane,
	fakeDatapath.Cell,
	monitorAgent.Cell,

	// Stub the 7 REST API handlers normally provided by real datapath cells.
	cell.Provide(newMockAPIHandlers),

	// Two more types real datapath provides; mirror what test/controlplane/suite/agent.go provides.
	cell.Provide(
		func() policymap.Factory { return nil },
		func() ctmap.GCRunner { return ctmap.NewFakeGCRunner() },
	),
	// ForwardableIPManager + neighbor test config — same as test/controlplane pattern.
	neighbor.ForwardableIPCell,
	cell.Provide(neighbor.NewCommonTestConfig(true, false, 100)),

	// DesiredRouteManager's reconciler.Cell wants a real StateDB reconciler
	// wired to the fake datapath's route table; since the fake datapath never
	// programs routes, stub it out exactly as test/controlplane/suite/agent.go
	// does for its dry-run agent hive.
	reconciler.TableCell,
	cell.Provide(func() (_ statedbReconciler.Reconciler[*reconciler.DesiredRoute]) { return nil }),

	// bandwidth.Cell (Linux EDT pacing) is not part of the fake datapath and
	// requires host network interfaces the mock agent never touches; provide
	// its Config as a disabled default so configureAPIServer's dependents
	// resolve without pulling in the real bandwidth manager.
	cell.Provide(func() bandwidthTypes.Config { return bandwidthTypes.Config{} }),

	// subnet.Cell's watcher wants the real ENI/subnet StateDB table, which is
	// cloud-provider-IPAM-specific and out of scope for the mock agent.
	cell.Provide(func() statedb.RWTable[subnet.SubnetTableEntry] { return nil }),
)

// Silence go-openapi import on environments where it's already vendored.
var _ = server.Spec{}

func main() {
	// Set DryMode globally BEFORE the cobra Run function executes initEnv.
	// DryMode is checked throughout Cilium ("Do not create BPF maps, devices, ..")
	// and is the canonical way to short-circuit datapath setup. Tests do exactly
	// this; we do it programmatically because DryMode is not exposed as a CLI flag.
	option.Config.DryMode = true

	hiveFn := func() *hive.Hive { return hive.New(MockAgent) }
	cmd.Execute(cmd.NewAgentCmd(hiveFn))
}

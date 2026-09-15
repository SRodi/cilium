// Command mock-cilium-agent runs the REAL Cilium agent control plane with the
// FAKE datapath cell, against an AKS-managed kube-apiserver.
//
// This is Approach B per the spike-mock-agent-prototype findings: instead of
// re-implementing cilium-agent's control-plane logic, we vendor the actual
// Cilium source (mock-clustermesh/cilium-fork/) and compose its
// `Infrastructure + ControlPlane` cells with the upstream-provided
// `pkg/datapath/fake.Cell` (which Cilium uses for its own integration tests).
//
// This guarantees byte-perfect wire compatibility with real Cilium agents in
// peer clusters because we ARE real Cilium — we just don't load BPF, iptables,
// envoy, hubble, or any other dataplane.
package main

import (
	"github.com/cilium/cilium/api/v1/server"
	daemonapi "github.com/cilium/cilium/api/v1/server/restapi/daemon"
	prefilterapi "github.com/cilium/cilium/api/v1/server/restapi/prefilter"
	"github.com/cilium/cilium/daemon/cmd"
	fakeDatapath "github.com/cilium/cilium/pkg/datapath/fake"
	"github.com/cilium/cilium/pkg/datapath/neighbor"
	datapathTypes "github.com/cilium/cilium/pkg/datapath/types"
	"github.com/cilium/cilium/pkg/hive"
	"github.com/cilium/cilium/pkg/maps/ctmap"
	"github.com/cilium/cilium/pkg/maps/policymap"
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

	GetMapHandler            daemonapi.GetMapHandler
	GetMapNameHandler        daemonapi.GetMapNameHandler
	GetMapNameEventsHandler  daemonapi.GetMapNameEventsHandler
	GetNodeIdsHandler        daemonapi.GetNodeIdsHandler
	GetPrefilterHandler      prefilterapi.GetPrefilterHandler
	PatchPrefilterHandler    prefilterapi.PatchPrefilterHandler
	DeletePrefilterHandler   prefilterapi.DeletePrefilterHandler
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
		func() datapathTypes.BandwidthConfig { return datapathTypes.DefaultBandwidthConfig },
	),
	// ForwardableIPManager + neighbor test config — same as test/controlplane pattern.
	neighbor.ForwardableIPCell,
	cell.Provide(neighbor.NewCommonTestConfig(true, false)),
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

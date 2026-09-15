# mock-cilium-agent

**Status: builds and runs.** Verified against `cilium/cilium` at commit
`199fb4f743` (`go build ./cmd/mock-cilium-agent/...` succeeds; the resulting
binary runs successfully as a Kubernetes workload — see "Verified
capabilities & limitations" below).

## Purpose

Run Cilium's real control-plane code (the `daemon/cmd.Infrastructure` +
`daemon/cmd.ControlPlane` hive cells) against a real kube-apiserver, but with
the real dataplane (`pkg/datapath/linux`) swapped for the upstream
`pkg/datapath/fake` cell that Cilium's own controlplane test suite
(`test/controlplane/suite`) uses. The goal is to exercise identity
allocation, CiliumEndpoint/CiliumNetworkPolicy watching and validation,
service/endpoint reconciliation, and other control-plane behavior without
needing a real node, kernel BPF support, or Envoy — while staying
byte-compatible with real Cilium because it *is* real Cilium code.

`main.go` composes: `cmd.Infrastructure`, `cmd.ControlPlane`,
`pkg/datapath/fake.Cell`, plus a handful of stub providers (REST API
handlers, policy map factory, route reconciler, bandwidth config, subnet
table) copied from `test/controlplane/suite/agent.go` — the canonical
reference for what real datapath cells provide that the fake datapath does
not.

## Why this needs re-adaptation on every Cilium version bump

This composes internal, non-API-stable Cilium packages (`daemon/cmd`,
`pkg/datapath/...`, hive cell wiring). Refactors to any of those — cell
renames, signature changes, new required dependencies in the hive object
graph — will break this build. That is expected and by design: **the
correct fix is always "make `cmd/mock-cilium-agent/main.go` compile against
the new source", not "pin to an old cilium/cilium version forever."**
`test/controlplane/suite/agent.go` is the fastest way to find the current
correct wiring pattern, since upstream keeps it in sync with `daemon/cmd`
cell changes as part of normal CI.

### Adaptation log (this round)

Going from a stale prior draft to a build against the current source
required 6 fixes, all found via `go build` errors and cross-checked against
`test/controlplane/suite/agent.go`:

1. `datapathTypes.BandwidthConfig`/`DefaultBandwidthConfig` no longer exist —
   bandwidth config moved to `pkg/datapath/linux/bandwidth/types.Config`.
2. `neighbor.NewCommonTestConfig` gained a third `neighborCalcRatelimit
   int64` parameter.
3. `configureAPIServer`'s dependents needed `bandwidthTypes.Config` provided
   (bandwidth.Cell isn't part of the fake datapath).
4. `reconciler.DesiredRouteManager` (route reconciler) needed a nil
   `statedbReconciler.Reconciler[*reconciler.DesiredRoute]` stub.
5. `subnet.registerSubnetWatcher` needed a nil
   `statedb.RWTable[subnet.SubnetTableEntry]` stub (cloud-IPAM-specific,
   out of scope here).
6. None of the above are things the mock's own code intentionally
   diverges on — they are exactly what real per-cell dependencies became in
   this version of Cilium.

## Build & release process (repeatable)

```
cmd/mock-cilium-agent/build.sh [--push] [--registry REGISTRY]
```

- Builds a plain Go binary (no BPF/Envoy toolchain needed — see
  `cmd/mock-cilium-agent/Dockerfile`) from whatever `cilium/cilium` commit is
  currently checked out.
- Tags the image with `git describe --tags --always --dirty` **and**
  `sha-<short-sha>`, so the exact upstream source state that produced any
  given image is always recoverable from its tag — no more ad hoc,
  untraceable pushes.
- To rebuild for a new Cilium release: check out (or rebase this branch
  onto) the desired `cilium/cilium` commit/tag, fix whatever `go build`
  reports (see "Adaptation log" above for the kind of fix expected), then
  re-run `build.sh`.
- Default registry: `ghcr.io/srodi/mock-cilium-agent` (override with
  `--registry` or `$REGISTRY`).

## Deploying for testing

Manifests live in the `cilium-synthetic` repo under `deploy/`:
`00-namespace.yaml`, `mock-agent-clusterrole.yaml` (the **real** Cilium
`ClusterRole`, reused verbatim so the mock agent is tested against the same
permission surface as production), `01-rbac-binding.yaml`,
`02-deployment.yaml`.

Verified minimum requirements for the container to start successfully
(all discovered empirically, see "Verified capabilities & limitations"):

- `securityContext.runAsUser: 0` — `pkg/common` has a hard `euid==0` check,
  independent of DryMode.
- `capabilities.add: [SYS_RESOURCE, BPF, SYS_ADMIN, NET_ADMIN]` — needed for
  `rlimit.RemoveMemlock()` and the real BPF map creation described below.
- A `hostPath` mount of the node's `/sys/fs/bpf` — `daemon_main.go`
  unconditionally calls `bpf.CheckOrMountFS()`; if bpffs is already mounted
  there (true on any node already running real Cilium) it's detected and
  reused instead of re-mounted, avoiding the need for a mount syscall.
- An `emptyDir` (or hostPath) at `/var/lib/cilium` containing an
  `include/bpf` subdirectory — `daemon_main.go` fatals if `BpfDir` doesn't
  exist, regardless of DryMode.
- The real Cilium `ClusterRole` permissions, plus a `configmaps` watch in
  `kube-system` for `cilium-config` (see Limitations).

Deployed today as a `Deployment` with 1 replica for testing (see
"Open design question" below on `DaemonSet` vs `Deployment`).

## Verified capabilities & limitations

Tested by deploying the built image into a 4-node kind cluster
(`kind-srodi-cilium`) that already runs real Cilium as its CNI, alongside
the pre-existing `fqdn-test-policy` CiliumNetworkPolicy and
`fqdn-test-pod`/`fqdn-test-pod` workloads from earlier FQDN cache testing.

### Confirmed working (real control-plane behavior, same code as production)

- **K8s object watching**: real informers for Pods, Nodes, Services,
  EndpointSlices — confirmed via debug logs and cache-synced events.
- **Cilium CRD watching**: CiliumNode, CiliumIdentity, CiliumEndpoint,
  CiliumNetworkPolicy, CiliumClusterwideNetworkPolicy, CiliumCIDRGroup, and
  upstream `networking.k8s.io/v1.NetworkPolicy` all reach "cache synced".
- **Identity allocation (CRD mode)**: a real `CiliumIdentity` was created
  for the mock agent's own test pod (`kubectl get ciliumidentities` showed a
  new numeric identity in the `mock-cilium-test` namespace).
- **CiliumEndpoint lifecycle**: a real `CiliumEndpoint` object was created
  and reported `ENDPOINT STATE: ready` with the pod's real IP — this is the
  actual endpoint-manager code path, not a stub.
- **CiliumNetworkPolicy watch/validation pipeline**: the mock agent picked
  up the pre-existing `fqdn-test-policy` CNP from the `default` namespace on
  its own (not something it manages) purely because CNPs are cluster-wide
  watched resources — proving policy-repository wiring works independent of
  per-node ownership.
- **Service/EndpointSlice → load-balancer reconciliation**: real backend/
  service-slot/RevNat updates logged for `kubernetes`, `kube-dns`,
  `hubble-peer`, confirming the loadbalancer control loop runs end-to-end.
- **Coexistence with a real per-node Cilium agent**: ran successfully on a
  kind node that already has real Cilium's own DaemonSet pod active,
  without resource conflicts, once RBAC/capabilities/mounts above were
  supplied — validates the coexistence assumption needed for any
  same-node deployment model.

### Confirmed NOT working / stubbed (by design or by gap)

- **No BPF program loading, no packet enforcement**: `DryMode` skips
  attaching BPF programs and building the real dataplane; policy is
  computed and revisions bump, but nothing is enforced on the wire. This is
  intentional and acceptable for control-plane-only testing.
- **No Envoy / L7 proxy / DNS proxy**: explicitly disabled
  (`--enable-l7-proxy=false`); `toFQDNs` / L7 HTTP policy rules cannot be
  exercised end-to-end (DNS-based identity population, the actual subject
  of the earlier FQDN cache test, will not work here).
- **Not actually BPF-free** (contradicts the original design intent): real
  BPF maps (`pkg/maps/metricsmap`, the loadbalancer reconciler's `lbmap`,
  `ratelimitmap`) are created via real `bpf(2)` syscalls regardless of
  DryMode, and `daemon_main.go` unconditionally mounts/verifies bpffs. This
  means the mock agent is **not** a pure userspace process — it needs
  `CAP_BPF`/`CAP_SYS_ADMIN`/`CAP_SYS_RESOURCE` and a live bpffs, which is a
  real gap vs. the "no BPF, no dataplane at all" premise in earlier design
  notes. Fixing this properly would mean stubbing those specific map
  constructors too; not attempted this round.
- **RBAC gaps found by testing, not by inspection**: needs a `configmaps`
  watch/list on `cilium-config` in `kube-system` even when the agent itself
  runs in a different namespace (real Cilium normally colocates with that
  configmap). The test cluster was also missing the `CiliumPodIPPool` CRD
  entirely — an environment gap, not a mock-agent gap, but it does mean
  multi-pool IPAM policy testing isn't possible without installing that CRD.
- **CiliumNetworkPolicy validation failure not yet root-caused**: the
  pre-existing `fqdn-test-policy` was flagged `Invalid CiliumNetworkPolicy`
  by the mock agent (while real Cilium's own agent reports it `VALID: True`
  for the same object). The log line
  (`pkg/policy/k8s/cilium_network_policy.go:59`) doesn't attach the
  underlying `cnp.Validate()` error, so the exact incompatibility is still
  unknown. **Do not treat "policy accepted" as validated for this mock
  until this is root-caused.**

### Open design question: DaemonSet vs Deployment

The verified coexistence result above is a positive signal for running as a
real per-node `DaemonSet` (matching real Cilium's deployment mode, which is
what's needed for realistic network-policy/control-plane fidelity testing).
The current `deploy/02-deployment.yaml` in `cilium-synthetic` uses a
`Deployment` with 1 replica purely because that was the fastest way to
validate the object graph and RBAC/capability requirements above — it is
not a considered decision against `DaemonSet` mode. Converting to
`DaemonSet` mainly means: node-scoping via `--node-name`/`hostNetwork`
considerations, and deciding whether the mock agent replaces or coexists
with the real per-node agent's ownership of that node's `CiliumNode`
object (two agents both trying to own the same node's identity/IPAM state
will conflict). This needs a decision before broader use and is intentionally
left open pending review.

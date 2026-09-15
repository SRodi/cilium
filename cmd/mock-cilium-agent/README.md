# mock-cilium-agent

**Status: design-only. No code yet.** This README is the contract that the future Go binary must satisfy.

## Purpose

Replace `cilium-agent`'s control-plane work for Pods running on **virtual Nodes** (kubemark hollow-kubelet). Runs the real watch + decode + write path against real AKS-managed kube-apiserver and real ACNS clustermesh-apiserver etcds. Skips the dataplane entirely (no BPF, no iptables, no envoy, no hubble).

**Important deployment shape** — runs as a **Deployment** with `replicas = virtual_node_count`, not as a DaemonSet. Each Pod owns exactly one virtual Node (via `--node-name` flag) but runs **on a real worker Node** (where it actually has CPU/memory). The 1:1 agent-per-virtual-Node fidelity is preserved.

See [`../../docs/design.md`](../../docs/design.md) §4 for full architecture and rationale.

## CLI flags (planned)

| Flag | Default | Meaning |
|---|---|---|
| `--cluster-name` | (required) | Local cluster name, matches CiliumNode `cluster.cilium.io/name` |
| `--cluster-id` | (required) | uint32, matches real Cilium semantics |
| `--node-name` | (required) | Which virtual Node this Pod owns (e.g. `hollow-node-7`) |
| `--kubeconfig` | in-cluster | Path to local AKS apiserver kubeconfig |
| `--clustermesh-config-dir` | `/var/lib/cilium/clustermesh` | Per-peer config files (real Cilium format) |
| `--metrics-listen-address` | `:9090` | Prometheus endpoint |
| `--processing-latency-distribution` | `lognormal:mean=2ms,p99=20ms` | Simulated per-event processing cost |
| `--inject-sync-error-rate` | `0.001` | Probability of injecting an etcd sync error per op |
| `--cilium-identity-allocator-mode` | `crd` | Must match cilium-operator config |
| `--log-level` | `info` | One of `debug`, `info`, `warn`, `error` |

### Alternative deployment mode: fat multiplexed

If memory pressure forces it (see design.md §8.4), we can run **one** mock-cilium-agent Pod per cluster that multiplexes all virtual Nodes. Activated via `--multiplex-virtual-nodes=hollow-node-0,hollow-node-1,...,hollow-node-99` instead of `--node-name`. Loses per-Pod CPU/memory metric fidelity (apiserver-side metrics are unchanged) — only adopt if Spike #4 shows 1:1 doesn't fit.

## Metric contract

The binary MUST emit, with **exact** real-Cilium names + label sets:

### Kvstoremesh series — emitted by REAL ACNS kvstoremesh sidecar, not the mock
The headline `cilium_kvstoremesh_*` metrics come from the **real** kvstoremesh sidecar inside the real ACNS clustermesh-apiserver Pod. mock-cilium-agent does NOT emit these — they're product-side metrics.

### Mock-agent-emitted series
- `cilium_endpoint_state{endpoint_state}` — `endpoint_state` ∈ `{ready, regenerating, ...}`
- `cilium_identity` (gauge)
- `cilium_clustermesh_global_services` (gauge)
- `cilium_kvstore_operations_duration_seconds_*` — agent-side etcd write latency (different from kvstoremesh's series)

### Process collector (free, just `promhttp.Handler()`)
- `cilium_process_cpu_seconds_total`
- `cilium_process_resident_memory_bytes`
- standard Go `go_*` and `process_*` defaults

### Stubbed-to-zero (intentionally — surface drift, don't hide it)
- `cilium_endpoint_regenerations_count` — emit `0` always
- `cilium_policy_regeneration_time_stats_seconds_*` — emit empty histogram
- `cilium_drop_count_total` — emit `0`
- `cilium_bpf_map_pressure` — emit `0`

See [`../../docs/scenario-coverage.csv`](../../docs/scenario-coverage.csv) for the per-metric fidelity rating.

## Wire-format compatibility

Writes the following kvstore key paths against the **real ACNS** clustermesh-apiserver etcd:

| Path | Value | Origin |
|---|---|---|
| `cilium/state/identities/v1/<id>` | label set, JSON | Cilium label hash → numeric ID |
| `cilium/state/services/v1/<cluster>/<ns>/<name>` | ServiceObj proto | Service object |
| `cilium/state/services/v1/<cluster>/<ns>/<name>/backends/<addr>` | BackendObj | EndpointSlice backend |
| `cilium/state/nodes/v1/<cluster>/<name>` | NodeObj | CiliumNode |
| `cilium/state/ip/v1/<cluster>/<ip>` | EndpointInfo | Pod IP → identity binding |

**Pinned to Cilium v1.16.x** for wire-format stability. CI test must round-trip a real cilium-agent's write through the mock's decoder (and vice versa) — this is the contract we cannot break.

## Coexistence with AKS-installed cilium-agent

The real AKS cilium-agent DaemonSet runs on real Nodes and handles real-Node Pods (the harness — clustermesh-apiserver, hollow-kubelet Pods, mock-cilium-agent Pods themselves). It is also scheduled onto virtual Nodes (DaemonSet semantics) but hollow-kubelet acks the Pod as Running without executing the binary — a no-op phantom.

mock-cilium-agent does NOT manage real-Node Pods. It filters its informers to Pods scheduled on its assigned `--node-name` (always a virtual Node). The two layers never write to the same kvstore keys (real agent writes for real-Node Pods, mock agent for virtual-Node Pods, keyed by Pod IP which is unique).

Spike #1 (design.md §8.1) validates this coexistence in practice.

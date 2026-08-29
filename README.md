# Hoshino

Hoshino manages a disposable, node-local Bazel native disk cache. It observes
completed cache entries with Linux inotify, keeps an approximate hot set with
HeavyKeeper, and selects cold entries when either the cache size or node free
space crosses a configured watermark.

Hoshino is not a remote-cache server. Bazel reads and writes the mounted
filesystem directly with `--disk_cache`. A separate remote cache remains the
cross-node fallback.

## Safety model

- Only lowercase SHA-256 entries matching `ac/<shard>/<digest>` or
  `cas/<shard>/<digest>` are candidates.
- `tmp`, `gc`, directories, symlinks, sockets, and unknown layouts are ignored.
- Linux deletion opens every parent with `O_NOFOLLOW`, verifies the cache-root
  identity, and rechecks file type, device, inode, size, and mtime immediately
  before `unlinkat`.
- Bazel invocations hold a shared `flock`; Hoshino requires a non-blocking
  exclusive lock before an active deletion cycle.
- Watcher loss freezes deletion, rebuilds complete watch coverage, resets the
  heat model, and starts a new warm-up interval.
- Shadow mode is enabled by default and never deletes entries.
- Hoshino never receives remote-cache credentials and never deletes remote
  objects.

## Build and test

Bazel 9.2.0 is the authoritative entrypoint:

```sh
bazel test //...
bazel build //:image
```

The OCI image uses a digest-pinned distroless non-root base. Merges to `master`
publish `ghcr.io/hawkingrei/hoshino:<commit-sha>`; consumers must resolve and
pin the resulting image digest.

## Runtime contract

Mount one node-local cache root into one Hoshino instance and the eligible
private Bazel Pods on that node. Do not run multiple Hoshino instances against
the same cache root.

Hoshino defaults match the initial Prow pilot:

- shadow mode enabled;
- 400 GiB cache high watermark and 320 GiB target;
- 30 percent node free-space floor and 35 percent target;
- 4 GiB protected action-cache budget;
- one-hour minimum residence and 30-minute warm-up.

Example daemon arguments:

```text
--cache-dir=/var/cache/bazel
--activity-lock=/var/cache-control/bazel-cache.activity.lock
--checkpoint-file=/var/cache-control/hoshino-state/hot-set.json
--shadow=true
```

Every Bazel command that uses the shared cache must hold the shared lease for
the entire invocation:

```sh
flock --shared /var/cache-control/bazel-cache.activity.lock \
  bazel test \
    --disk_cache=/var/cache/bazel \
    --remote_cache=https://storage.googleapis.com/bazel-cache-nowledge \
    --google_default_credentials \
    //...
```

The metrics listener exposes `/healthz`, `/readyz`, `/metrics`, and the legacy
`/prometheus` path on port 9092. Pprof is disabled by default; when explicitly
enabled it binds to loopback unless configured otherwise.

Active deletion requires `--shadow=false`. Enable it only after the shadow
pilot has satisfied its observation gates and the same cache path is protected
by the shared Bazel wrapper.

## Provenance

The project contains code originally derived from Kubernetes and Go inotify
implementations. File-level copyright headers are retained. See [NOTICE](NOTICE)
and [LICENSE](LICENSE).

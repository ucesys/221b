#!/usr/bin/env python3
"""Fills a cluster running kwok with fake nodes and pods, for the scale test.

Talks to the API server through `kubectl proxy` (default 127.0.0.1:8001)
with many requests in flight, since kubectl itself creates objects one at
a time.

    kubectl proxy --port=8001 &
    hack/fill-kwok.py --nodes 500 --pods 15000
"""
import argparse
import json
import random
import sys
import time
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor

ZONES = ["zone-a", "zone-b", "zone-c"]


def request(base, method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + path, data=data, method=method, headers={"Content-Type": "application/json"})
    for attempt in range(8):
        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                return json.load(resp)
        except urllib.error.HTTPError as e:
            if e.code == 409:  # already exists: a rerun
                return None
            if e.code in (429, 500, 503, 504) and attempt < 7:
                time.sleep(0.5 * (attempt + 1))
                continue
            raise RuntimeError(f"{method} {path}: {e.code} {e.read()[:300]!r}")
        except (urllib.error.URLError, TimeoutError, ConnectionError):
            if attempt == 7:
                raise
            time.sleep(0.5 * (attempt + 1))


def node(i):
    name = f"kwok-node-{i:03d}"
    big = i % 5 == 4
    cpu, mem = ("32", "256Gi") if big else ("16", "64Gi")
    labels = {
        "kubernetes.io/hostname": name,
        "kubernetes.io/os": "linux",
        "kubernetes.io/arch": "amd64",
        "topology.kubernetes.io/zone": ZONES[i % 3],
        "node.kubernetes.io/instance-type": "memory-32x256" if big else "general-16x64",
        "type": "kwok",
    }
    taints = [{"key": "kwok.x-k8s.io/node", "value": "fake", "effect": "NoSchedule"}]
    if big:
        labels["workload"] = "analytics"
        taints.append({"key": "dedicated", "value": "analytics", "effect": "NoSchedule"})
    return {
        "apiVersion": "v1",
        "kind": "Node",
        "metadata": {"name": name, "labels": labels, "annotations": {"kwok.x-k8s.io/node": "fake", "node.alpha.kubernetes.io/ttl": "0"}},
        "spec": {"podCIDR": f"10.{100 + i // 250}.{i % 250}.0/24", "taints": taints},
        "status": {
            "allocatable": {"cpu": cpu, "memory": mem, "pods": "110"},
            "capacity": {"cpu": cpu, "memory": mem, "pods": "110"},
            "nodeInfo": {
                "architecture": "amd64",
                "operatingSystem": "linux",
                "osImage": "Ubuntu 24.04.3 LTS",
                "kernelVersion": "6.8.0-60-generic",
                "containerRuntimeVersion": "containerd://2.1.4",
                "kubeletVersion": "v1.37.0",
                "kubeProxyVersion": "v1.37.0",
                "bootID": "",
                "machineID": "",
                "systemUUID": "",
            },
            "phase": "Running",
        },
    }


def pod(i, nodes, rnd):
    ns = f"team-{i % 10}"
    n = nodes[i % len(nodes)]
    cpu = rnd.choice(["50m", "100m", "200m", "250m", "500m"])
    mem = rnd.choice(["64Mi", "128Mi", "256Mi", "512Mi"])
    resources = {"requests": {"cpu": cpu, "memory": mem}}
    if i % 3 == 0:
        resources["limits"] = {"cpu": "1", "memory": "1Gi"}
    return ns, {
        "apiVersion": "v1",
        "kind": "Pod",
        "metadata": {"name": f"app-{i:05d}", "namespace": ns, "labels": {"app": f"app-{i % 300}"}},
        "spec": {
            "nodeName": n,
            "tolerations": [{"operator": "Exists"}],
            "containers": [{"name": "app", "image": "registry.k8s.io/pause:3.10", "resources": resources}],
        },
    }


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--nodes", type=int, default=500)
    p.add_argument("--pods", type=int, default=15000)
    p.add_argument("--proxy", default="http://127.0.0.1:8001")
    p.add_argument("--workers", type=int, default=48)
    a = p.parse_args()
    rnd = random.Random(1)

    start = time.time()
    with ThreadPoolExecutor(a.workers) as pool:
        list(pool.map(lambda i: request(a.proxy, "POST", "/api/v1/nodes", node(i)), range(a.nodes)))
    print(f"{a.nodes} nodes created in {time.time() - start:.1f}s", flush=True)

    for t in range(10):
        request(a.proxy, "POST", "/api/v1/namespaces", {"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": f"team-{t}"}})
    names = [f"kwok-node-{i:03d}" for i in range(a.nodes)]
    start = time.time()
    pods = [pod(i, names, rnd) for i in range(a.pods)]
    with ThreadPoolExecutor(a.workers) as pool:
        list(pool.map(lambda np: request(a.proxy, "POST", f"/api/v1/namespaces/{np[0]}/pods", np[1]), pods))
    print(f"{a.pods} pods created in {time.time() - start:.1f}s", flush=True)


if __name__ == "__main__":
    sys.exit(main())

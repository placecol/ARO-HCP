#!/usr/bin/env bash
# Repeatedly creates and deletes an OOM-killing test pod to generate
# recurring KubeContainerOOMKilled alerts, 30 minutes apart.
set -euo pipefail

CONTEXT="pers-usw3cpla-svc"

while true; do
  kubectl --context "$CONTEXT" apply -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: oomtest
  namespace: default
spec:
  containers:
  - name: oomtest
    image: docker.io/library/postgres:16
    env:
    - name: POSTGRES_PASSWORD
      value: test
    resources:
      limits:
        memory: "16Mi"
      requests:
        memory: "16Mi"
EOF

  sleep 1800

  kubectl --context "$CONTEXT" delete pod oomtest

  sleep 1800
done

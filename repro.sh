#!/bin/bash

export MGMT_RG=hcp-underlay-pers-usw3cpla-mgmt-1
export MGMT_NP_RG=hcp-underlay-pers-usw3cpla-mgmt-1-aks1
export MGMT_CLUSTER_NAME=pers-usw3cpla-mgmt-1
export NUM_CLUSTERS=2
FRONTEND_ADDRESS="${FRONTEND_ADDRESS:-http://localhost:8443}"
ADMIN_API_ADDRESS="${ADMIN_API_ADDRESS:-http://localhost:8444}"

echo "=== Phase 1: creating $NUM_CLUSTERS clusters in parallel ==="
declare -a test_pids
for i in $(seq 1 "$NUM_CLUSTERS"); do
    make -C /home/cplace/code/ARO-ME/ per/e2e/run TEST_NAME="Customer should be able to create an HCP cluster and custom node pool osDisk size" ARO_E2E_SKIP_CLEANUP=true > "./create_${i}.log" 2>&1 &
    test_pids+=($!)
done

fail=0
for pid in "${test_pids[@]}"; do
    wait "$pid" || fail=1
done
if [ "$fail" -ne 0 ]; then
    echo "one or more cluster creations failed -- check create_*.log" >&2
    exit 1
fi

echo "=== Phase 2: discover all clusters + CP namespaces ==="
mapfile -t HCP_ITEMS < <(kubectl --context pers-usw3cpla-mgmt-1 get namespaces -o json | jq -c '.items[] | select(.metadata.annotations["azure.microsoft.com/hcp-cluster-azure-resource-id"] != null) | {namespace: .metadata.name, resourceId: .metadata.annotations["azure.microsoft.com/hcp-cluster-azure-resource-id"]}')
if [ "${#HCP_ITEMS[@]}" -eq 0 ]; then
    echo "no HCP clusters found" >&2
    exit 1
fi

declare -a CLUSTER_RESOURCE_IDS
declare -a CP_NAMESPACES
for item in "${HCP_ITEMS[@]}"; do
    resource_id=$(echo "$item" | jq -r '.resourceId')
    base_ns=$(echo "$item" | jq -r '.namespace')
    CLUSTER_RESOURCE_IDS+=("$resource_id")
    echo $resource_id
    CP_NAMESPACES+=("${base_ns}")
done

echo "Clusters:"
printf '  %s\n' "${CLUSTER_RESOURCE_IDS[@]}"
echo "CP namespaces:"
printf '  %s\n' "${CP_NAMESPACES[@]}"

echo "=== Phase 3: find the node hosting the most CSI-volume pods across ALL CP namespaces ==="
for ns in "${CP_NAMESPACES[@]}"; do
    kubectl get pods -n "$ns" -o json 2>/dev/null | jq -r '
      .items[] |
      select(.spec.volumes[]? | .csi?.driver == "secrets-store.csi.k8s.io") |
      .spec.nodeName
    '
done > /tmp/repro_node_counts.txt
TARGET_NODE=$(sort /tmp/repro_node_counts.txt | uniq -c | sort -rn | head -1 | awk '{print $2}')

if [ -z "$TARGET_NODE" ]; then
    echo "could not determine a target node (no CSI-volume pods found across CP namespaces)" >&2
    exit 1
fi

export NODE_TO_STOP_VMSS="$(echo $TARGET_NODE | awk -F '-vmss' '{print $1}')-vmss"
export NODE_TO_STOP_INSTANCE_ID=$(echo $TARGET_NODE | awk -F '-vmss' '{print $2}')
export NODE_TO_STOP_INSTANCE_ID=$((10#$NODE_TO_STOP_INSTANCE_ID))
export NODE_POOL=$(echo $TARGET_NODE| awk -F '-' '{print $2}')

echo "Target node: $TARGET_NODE (vmss=$NODE_TO_STOP_VMSS instance=$NODE_TO_STOP_INSTANCE_ID pool=$NODE_POOL)"
echo "Hits per node:"
sort /tmp/repro_node_counts.txt | uniq -c | sort -rn

echo "=== Phase 4: stop/start the shared node ==="
az aks nodepool update -g "$MGMT_RG" --cluster-name "$MGMT_CLUSTER_NAME" --name "$NODE_POOL" --disable-cluster-autoscaler
az vmss stop -g "$MGMT_NP_RG" --name "$NODE_TO_STOP_VMSS" --instance-ids "$NODE_TO_STOP_INSTANCE_ID"
NODE_STOP_WAIT_SECONDS=$((RANDOM % 120 + 30))
echo "waiting ${NODE_STOP_WAIT_SECONDS} seconds before starting node..."
sleep $NODE_STOP_WAIT_SECONDS
az vmss start -g "$MGMT_NP_RG" --name "$NODE_TO_STOP_VMSS" --instance-ids "$NODE_TO_STOP_INSTANCE_ID" --no-wait

echo "=== Phase 5: delete all customer clusters ==="
for resource_id in "${CLUSTER_RESOURCE_IDS[@]}"; do
    curl -sk -X DELETE "${FRONTEND_ADDRESS}${resource_id}?api-version=2025-12-23-preview"
done

echo "=== Phase 6: watch for stuck namespaces (20 min timeout) ==="
remaining=("${CP_NAMESPACES[@]}")
deadline=$(( $(date +%s) + 1200 ))
while [ "${#remaining[@]}" -gt 0 ] && [ "$(date +%s)" -lt "$deadline" ]; do
    next=()
    for ns in "${remaining[@]}"; do
        if kubectl get ns "$ns" &>/dev/null; then
            next+=("$ns")
        else
            echo "$ns deleted"
        fi
    done
    echo "$CP_NS deleted"
    remaining=("${next[@]}")
    [ "${#remaining[@]}" -gt 0 ] && sleep 5
done

if [ "${#remaining[@]}" -gt 0 ]; then
    echo ""
    echo "=== REPRO CANDIDATE: still-Terminating namespaces after 20 minutes ==="
    for ns in "${remaining[@]}"; do
        echo "--- $ns ---"
        kubectl get ns "$ns" -o jsonpath='{.status.phase}{"\n"}'
        echo "Secrets with finalizers (per repro.txt: the analogous ORC fix stripped a stuck finalizer from Secrets, not pods):"
        kubectl get secrets -n "$ns" -o json | jq -r '
          .items[] | select(.metadata.finalizers != null and (.metadata.finalizers|length) > 0) |
          "\(.metadata.name)\t\(.metadata.finalizers)"
        '
        echo "Pods with finalizers, for comparison:"
        kubectl get pods -n "$ns" -o json | jq -r '
          .items[] | select(.metadata.finalizers != null and (.metadata.finalizers|length) > 0) |
          "\(.metadata.name)\t\(.metadata.finalizers)"
        '
    done
    exit 1
else
    echo "all namespaces deleted cleanly -- no repro this run"
    echo "cleaning up customer resource groups"
    for item in "${CLUSTER_RESOURCE_IDS[@]}"; do
        rg_name=$(echo "$RESOURCE_ID" | awk -F'/' '{print $5}')
        az group delete --name $rg_name --yes --no-wait
    done
fi

# for i in $(seq 1 10); do
#     make -C /home/cplace/code/ARO-ME/ per/e2e/run TEST_NAME="Customer should create an HCP cluster and validate TLS certificates" ARO_E2E_SKIP_CLEANUP=true
#
#     export CUSTOMER_RG_NAME=$(hcpctl hcp list --output json | jq -r '.items[].resourceGroupName' | awk -F '--' '{print $1}')
#     export CLUSTER_NAME=$(curl -sk "http://localhost:8443/subscriptions/1d3378d3-5a3f-4712-85a1-2485495dfc4b/resourcegroups/${CUSTOMER_RG_NAME}/providers/Microsoft.RedHatOpenShift/hcpOpenShiftClusters?api-version=2024-06-10-preview" | jq -r '.value[].name')
#     source ./demo/env_vars
#     export CP_NS=$(kubectl get ns | grep -oE 'ocm-.+-.+-\S+')
#     export NODE_TO_STOP=$(kubectl get pods -n "$CP_NS" -o json | jq -r '
#       .items[] |
#       select(.spec.volumes[]? | .csi?.driver == "secrets-store.csi.k8s.io") |
#       "\(.spec.nodeName)"
#     ' | sort | uniq -c | sort -rn | head -1 | awk '{print $2}'
#     )
#
#     echo ""
#     echo "======================================="
#     echo "Beginning repro attempt:"
#     echo "CLUSTER_RESOURCE_ID: ${CLUSTER_RESOURCE_ID}"
#     echo "MGMT_RG: ${MGMT_RG}"
#     echo "MGMT_CLUSTER_NAME: ${MGMT_CLUSTER_NAME}"
#     echo "NODE_POOL: ${NODE_POOL}"
#     echo "NODE_TO_STOP_VMSS: ${NODE_TO_STOP_VMSS}"
#     echo "NODE_TO_STOP_INSTANCE_ID: ${NODE_TO_STOP_INSTANCE_ID}"
#     echo "NODE_TO_STOP_INSTANCE_ID: ${NODE_TO_STOP_INSTANCE_ID}"
#     echo "CP_NS: ${CP_NS}"
#     echo "======================================="
#
#     # 1. Ensure nodepool does not autoscale
#     # 2. Stop vmss
#     # 3. Wait for 60 sec
#     # 4. Start vmss instance
#     # 5. Initiate deletion of cluster
#     # 6. Watch cluster status, exit when cluster is finally deleted
#     az aks nodepool update -g $MGMT_RG --cluster-name $MGMT_CLUSTER_NAME --name $NODE_POOL --disable-cluster-autoscaler 
#     az vmss stop -g $MGMT_NP_RG --name $NODE_TO_STOP_VMSS --instance-ids $NODE_TO_STOP_INSTANCE_ID
#     sleep $((RANDOM % 120 + 30))
#     az vmss start -g $MGMT_NP_RG --name $NODE_TO_STOP_VMSS --instance-ids $NODE_TO_STOP_INSTANCE_ID --no-wait
#     curl -k -X DELETE "http://localhost:8443${CLUSTER_RESOURCE_ID}?api-version=2025-12-23-preview"
#
#     while kubectl get ns "$CP_NS" &>/dev/null; do
#         kubectl get ns "$CP_NS"
#         sleep 5
#     done
#     echo "$CP_NS deleted"
# done

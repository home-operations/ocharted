#!/usr/bin/env bash
# Flux end-to-end test: installs flux-operator + a FluxInstance into the
# current cluster (expected: kind), deploys ocharted with cosign signing enabled,
# and drives a cosign-verified OCIRepository + HelmRelease (podinfo, via its
# classic Helm repo) through the proxy. A wrong-key OCIRepository must fail
# verification — the admission-gate property the signing feature exists for.
#
# Expects KUBECONFIG pointing at a disposable cluster with internet egress,
# and the ocharted image available in-cluster (kind load) when
# OCHARTED_TEST_PULL_POLICY=Never. Run via `mise run flux-e2e`.
set -euo pipefail

# renovate: datasource=docker depName=ghcr.io/controlplaneio-fluxcd/charts/flux-operator
FLUX_OPERATOR_VERSION="0.57.0"

IMAGE_TAG="${OCHARTED_TEST_IMAGE_TAG:-latest}"
PULL_POLICY="${OCHARTED_TEST_PULL_POLICY:-IfNotPresent}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART_DIR="${SCRIPT_DIR}/../../../charts/ocharted"
WORKDIR="$(mktemp -d)"
trap 'rm -rf "${WORKDIR}"' EXIT

diagnose() {
    echo "=== DIAGNOSTICS ==="
    kubectl get pods -A || true
    kubectl -n podinfo get ocirepositories,helmreleases -o wide || true
    kubectl -n podinfo describe ocirepositories || true
    kubectl -n ocharted-e2e logs deploy/ocharted --tail=50 || true
    kubectl -n flux-system logs deploy/source-controller --tail=50 || true
    kubectl -n flux-system logs deploy/helm-controller --tail=50 || true
}
trap diagnose ERR

apply_ns() {
    kubectl create namespace "$1" --dry-run=client -o yaml | kubectl apply -f -
}

echo "--- Installing flux-operator ${FLUX_OPERATOR_VERSION}"
helm upgrade --install flux-operator \
    oci://ghcr.io/controlplaneio-fluxcd/charts/flux-operator \
    --version "${FLUX_OPERATOR_VERSION}" \
    --namespace flux-system --create-namespace --wait --timeout 5m

echo "--- Creating FluxInstance and waiting for the controllers"
kubectl apply -f "${SCRIPT_DIR}/fluxinstance.yaml"
kubectl -n flux-system wait fluxinstance/flux --for=condition=Ready --timeout=5m

echo "--- Generating the signing key pair (plus a wrong key for the negative test)"
openssl genpkey -algorithm ed25519 -out "${WORKDIR}/signing.key"
openssl pkey -in "${WORKDIR}/signing.key" -pubout -out "${WORKDIR}/cosign.pub"
openssl genpkey -algorithm ed25519 -out "${WORKDIR}/wrong.key"
openssl pkey -in "${WORKDIR}/wrong.key" -pubout -out "${WORKDIR}/wrong.pub"

echo "--- Deploying ocharted with signing enabled"
apply_ns ocharted-e2e
kubectl -n ocharted-e2e create secret generic ocharted-signing \
    --from-file=signing.key="${WORKDIR}/signing.key" \
    --dry-run=client -o yaml | kubectl apply -f -
helm upgrade --install ocharted "${CHART_DIR}" \
    --namespace ocharted-e2e --wait --timeout 5m \
    --set image.tag="${IMAGE_TAG}" \
    --set image.pullPolicy="${PULL_POLICY}" \
    --set signing.existingSecret=ocharted-signing
# Secret updates don't restart running pods, and ocharted reads the signing key
# once at startup — roll the deployment so reruns of this script (which
# regenerate the key) never leave a pod signing with a stale key.
kubectl -n ocharted-e2e rollout restart deployment/ocharted
kubectl -n ocharted-e2e rollout status deployment/ocharted --timeout=2m

echo "--- Creating the verified podinfo workload through ocharted"
apply_ns podinfo
kubectl -n podinfo create secret generic ocharted-pub \
    --from-file=cosign.pub="${WORKDIR}/cosign.pub" \
    --dry-run=client -o yaml | kubectl apply -f -
kubectl -n podinfo create secret generic wrong-pub \
    --from-file=cosign.pub="${WORKDIR}/wrong.pub" \
    --dry-run=client -o yaml | kubectl apply -f -
# Fresh objects each run: a previous run's objects carry accumulated failure
# backoff, which can push Flux's next retry past the wait windows below.
kubectl -n podinfo delete ocirepository,helmrelease --all --ignore-not-found --wait=true
kubectl apply -f "${SCRIPT_DIR}/podinfo.yaml"

echo "--- Waiting for the cosign-verified OCIRepository to become Ready"
kubectl -n podinfo wait ocirepository/podinfo --for=condition=Ready --timeout=3m
kubectl -n podinfo get ocirepository/podinfo \
    -o jsonpath='{.status.artifact.revision}{"\n"}'

echo "--- Waiting for the HelmRelease to install podinfo"
kubectl -n podinfo wait helmrelease/podinfo --for=condition=Ready --timeout=5m
kubectl -n podinfo rollout status deployment/podinfo --timeout=3m

echo "--- Negative test: verification against the wrong key must fail"
kubectl apply -f "${SCRIPT_DIR}/podinfo-badkey.yaml"
kubectl -n podinfo wait ocirepository/podinfo-badkey \
    --for=condition=Ready=False --timeout=3m
BADKEY_MSG="$(kubectl -n podinfo get ocirepository/podinfo-badkey \
    -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}')"
echo "podinfo-badkey: ${BADKEY_MSG}"
case "${BADKEY_MSG}" in
*verif* | *signature*) ;;
*)
    echo "expected a signature verification failure, got: ${BADKEY_MSG}"
    exit 1
    ;;
esac

echo "--- Flux e2e passed: verified pull, install, and wrong-key rejection"

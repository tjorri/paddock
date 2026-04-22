# Tiltfile — Paddock dev loop.
#
# Prereq: hack/kind-up.sh has been run (creates the paddock-dev Kind cluster
# and installs cert-manager). Then: `tilt up`.
#
# v0.1 M0: full docker build on each change (~30s). The kubebuilder-scaffolded
# Dockerfile is a multi-stage build that compiles the manager inside Docker.
# Once CRDs and controllers land, we'll revisit with a local-binary +
# live_update pattern for sub-10s reloads.

# Guardrail: only ever run against the local Kind cluster.
allow_k8s_contexts(['kind-paddock-dev'])

docker_build(
    'controller',
    context='.',
    dockerfile='Dockerfile',
    build_args={},
    ignore=['bin/', 'docs/', 'test/e2e/', '*.md'],
)

# Apply the standard kubebuilder deployment (rbac, manager, metrics service).
# CRDs and webhooks are added in later milestones and are uncommented in
# config/default/kustomization.yaml at that time.
k8s_yaml(kustomize('config/default'))

k8s_resource(
    'paddock-controller-manager',
    port_forwards=['8081:8081'],  # health/readyz
    labels=['paddock'],
)

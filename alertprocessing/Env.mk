ARO_HCP_IMAGE_ACR ?= {{ .acr.svc.name }}
ARO_HCP_IMAGE_REGISTRY ?= ${ARO_HCP_IMAGE_ACR}.{{ .acrDNSSuffix }}
ALERTPROCESSING_IMAGE_REPOSITORY ?= {{ .alertprocessing.image.repository }}

NAMESPACE ?= {{ .alertprocessing.k8s.namespace }}

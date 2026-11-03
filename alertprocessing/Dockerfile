ARG PLATFORM

# Builder image installs tools needed to build aro-hcp-alertprocessing
FROM --platform=${PLATFORM} mcr.microsoft.com/oss/go/microsoft/golang:1.26-azurelinux3.0 AS builder
ENV GOTOOLCHAIN=local
COPY internal/go.mod internal/go.sum internal/
COPY alertprocessing/go.mod alertprocessing/go.sum alertprocessing/
RUN cd alertprocessing && go mod download
WORKDIR /app
COPY internal/ internal/
COPY --exclude=alertprocessing/bin alertprocessing/ alertprocessing/

# https://github.com/microsoft/go/tree/microsoft/main/eng/doc/fips#build-option-to-require-fips-mode
ENV CGO_ENABLED=1 GOFIPS140=latest
RUN --mount=type=cache,target=/go/cache \
    GOCACHE=/go/cache \
    make --directory alertprocessing build

FROM --platform=${PLATFORM} mcr.microsoft.com/azurelinux/distroless/base:3.0
USER 65532:65532
WORKDIR /
COPY --from=builder /app/alertprocessing/bin/alertprocessing .
ARG ARO_HCP_REVISION
ARG ARO_HCP_COMMIT_TIME
LABEL vcs-ref="${ARO_HCP_REVISION}"
LABEL vcs-ref-time="${ARO_HCP_COMMIT_TIME}"
LABEL vcs-url="https://github.com/Azure/ARO-HCP"
ENTRYPOINT ["/alertprocessing", "controller"]

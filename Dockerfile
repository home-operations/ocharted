# syntax=docker/dockerfile:1

ARG GO_VERSION

# ---- Build ----------------------------------------------------------------
FROM golang:${GO_VERSION}-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=dev

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ocharted/ cmd/ocharted/
COPY internal/ internal/

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${REVISION}" \
    -o ocharted ./cmd/ocharted

# ---- Runtime --------------------------------------------------------------
# distroless/static:nonroot — the fleet-standard runtime base for our static
# (CGO_ENABLED=0) Go binaries: it bundles CA certs, /etc/passwd, and a nonroot
# user (uid/gid 65532), so no files need carrying over and no USER is needed.
FROM gcr.io/distroless/static:nonroot
COPY --from=builder /workspace/ocharted /ocharted
ENTRYPOINT ["/ocharted"]

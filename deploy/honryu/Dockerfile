# Builds any one of Honryu's own control-plane binaries (api, calibrator,
# scheduler, sidecar) from a single Dockerfile, selected via
# --build-arg CMD=<name>. They share one build stage because they share one
# module and one dependency set -- four near-identical Dockerfiles would
# only drift.
#
# Runtime base is alpine, not distroless: the k8s scheduler adapter's own
# sidecar container Command is ["/bin/sh", "-c", "/honryu-sidecar ..."]
# (internal/adapters/scheduler/k8s/k8s.go's sidecarScript) -- a shell-less
# distroless image would fail to even start that container. The binary is
# named /honryu-${CMD} to match that same hardcoded /honryu-sidecar path.
# Pinned by digest as well as tag: a tag is a moving pointer, so "golang:1.26.9"
# alone does not make this build reproducible, and a compromised or simply
# re-pushed tag would silently change the compiler that produces the binaries.
# Digest resolved with `docker buildx imagetools inspect` at pin time.
FROM golang:1.26.9@sha256:37cfcca63222ec499bec5e8ad7aad52dbf7529db36d5812b8eae2f0621aa92d6 AS build
ARG CMD=api
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/honryu-${CMD} ./cmd/${CMD}

FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
ARG CMD=api
# ARGs are build-time only; the running container needs $CMD at runtime,
# so it is re-exposed as an ENV (a bare exec-form JSON array does not
# expand ARG/ENV at all -- verified, it shipped the literal "${CMD}"
# string). ENTRYPOINT stays JSON-form (hadolint DL3025) by letting the
# shell expand it: sh -c runs, replaces itself with the binary via exec,
# so the binary is PID 1 and receives OS signals directly -- same signal
# behaviour shell-form promised, without its PID-1-is-a-shell pitfall.
ENV CMD=${CMD}
COPY --from=build /out/honryu-${CMD} /honryu-${CMD}
ENTRYPOINT ["/bin/sh", "-c", "exec /honryu-$CMD"]

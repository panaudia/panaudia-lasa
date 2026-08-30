# syntax=docker/dockerfile:1
#
# panaudia-lasa image: the panaudia-server binary. One file for every target: the build stage
# compiles the server from this repository (build context = repo root),
# the runtime stage carries the binary and libopus and nothing else.
#
#   docker buildx build --platform linux/amd64 -f docker/Dockerfile -t panaudia/panaudia-lasa:dev .
#
# A second target, `bench`, is the same runtime image carrying the
# server package's compiled test binary instead of the server, with
# the capacity ceiling harness (server/ceiling_test.go) as its
# entrypoint, so the per-node ceiling can be measured on the machine
# type that will run the image, with the image's own static libxsmm:
#
#   TARGET=bench docker/build                       # tag <version>-<arch>-bench
#   docker run --rm panaudia/panaudia-lasa:<v>-arm64-bench            # TestCeilingOpus
#   docker run --rm <img> -test.run TestCeilingCodecFree
#   docker run --rm -e CEILING_ORDER=5 -e CEILING_REVERB=medium-room <img>
#
# Only the ceiling tests are meant to run there: the rest of the
# package's tests read repository files the image does not carry.
#
# On both architectures the GEMM hot path is static libxsmm (`-tags
# xsmm`, JIT with runtime CPU dispatch — one image serves every x86
# generation, and the AArch64 JIT emits NEON/ASIMD kernels) embedded in
# the binary. The pure-Go gonum backend remains the in-binary fallback
# for any shape the JIT cannot serve; the output is sample-for-sample
# identical (engine/gemm parity tests), only the speed differs (the
# 2026-07 arm64 sweep had libxsmm 7-15x ahead of gonum on every
# production shape).
#
# Runtime configuration is environment variables only; see
# server/.env.example (also shipped at /opt/panaudia/.env.example). A
# .env file mounted at /opt/panaudia/.env, or anywhere with
# PANAUDIA_ENV_FILE pointing at it, is loaded at startup. The server
# prints its full effective configuration when it starts.
#
# Run with `--cap-add NET_ADMIN` (Kubernetes: capabilities.add:
# [NET_ADMIN]) and the server sizes its UDP buffers to what QUIC wants
# regardless of the host's net.core.rmem_max; without it, it runs on
# the host's limits and says so at startup (see the runtime stage).

ARG GO_VERSION=1.26.7

# --- build -------------------------------------------------------------
FROM golang:${GO_VERSION}-trixie AS builder

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      build-essential git ca-certificates pkg-config libopus-dev libcap2-bin \
 && rm -rf /var/lib/apt/lists/*

ARG TARGETARCH
# The version the binary reports and the image is tagged with; docker/build
# derives it from the server/v* git tag on the host (.git is not in the
# build context).
ARG VERSION=dev

# libxsmm, static, pinned, every architecture. The libxsmm_dispatch_gemm
# API the engine uses exists only on the unreleased 2.0 line, hence a
# SHA, not a release; bump deliberately and re-run the GEMM parity tests.
# STATIC=1: embedded in the Go binary, no runtime library. BLAS=0: no
# fallback deps.
ARG LIBXSMM_REF=1d7138cac9f446284daeaf65041cc9043cec2a3b
RUN git clone https://github.com/libxsmm/libxsmm.git /tmp/libxsmm \
 && cd /tmp/libxsmm \
 && git checkout "${LIBXSMM_REF}" \
 && make -j"$(nproc)" STATIC=1 BLAS=0 \
 && make PREFIX=/usr/local STATIC=1 install \
 && cd / && rm -rf /tmp/libxsmm

WORKDIR /src
# The server module reaches the engine through `replace ../engine`, so
# both modules travel together. Any local go.work is excluded by
# .dockerignore and GOWORK=off; the build must not depend on sibling
# checkouts.
ENV GOWORK=off CGO_ENABLED=1
COPY engine/go.mod engine/go.sum engine/
COPY server/go.mod server/go.sum server/
RUN cd server && go mod download

COPY engine engine
COPY server server
COPY LICENSE third-party-licences.md ./

# nolibopusfile: hraban/opus's streaming API (unused here) would
# otherwise pull in libopusfile; the tag drops it from build and image.
# Not stripped (-s -w): the libxsmm guard below reads the symbol table,
# and the binary also keeps its Go build info (go version -m).
RUN cd server \
 && go build -tags nolibopusfile,xsmm -trimpath -ldflags "-X main.version=${VERSION}" -o /out/panaudia-server . \
 && { go tool nm /out/panaudia-server | grep -q libxsmm_dispatch_gemm \
      || { echo "ERROR: ${TARGETARCH} binary does not embed libxsmm (built without -tags xsmm?)" >&2; exit 1; }; } \
 && echo "OK: static libxsmm embedded (${TARGETARCH})"

# The bench target's payload: the server package's tests as one binary,
# same tags and GEMM backend as the server (the nm guard again — the
# ceiling number is meaningless on the gonum fallback).
RUN cd server \
 && go test -c -tags nolibopusfile,xsmm -trimpath -o /out/server.test . \
 && { go tool nm /out/server.test | grep -q libxsmm_dispatch_gemm \
      || { echo "ERROR: test binary does not embed libxsmm" >&2; exit 1; }; }

# CAP_NET_ADMIN, permitted only (+p, never +e): a run with
# `--cap-add NET_ADMIN` lets the server force the QUIC socket buffers
# past net.core.rmem_max (server/udpbuf.go: bind, force, re-exec with
# the socket inherited and no capability left), and a run without it
# starts exactly as before — the kernel refuses exec only for
# unsatisfiable *effective* file caps. COPY --from preserves the
# xattr, so the runtime layer is unchanged.
RUN setcap cap_net_admin+p /out/panaudia-server \
 && getcap /out/panaudia-server | grep -q cap_net_admin=p \
 || { echo "ERROR: file capability not set" >&2; exit 1; }

# --- runtime -----------------------------------------------------------
# `base` is the runtime image proper; `runtime` (the default target,
# last in the file) re-exposes it unchanged, and `bench` layers the test
# binary on top.
FROM debian:trixie-slim AS base

# perl-base is Debian "Essential" and so present in every image, but
# nothing here runs it (the entrypoint is the Go binary, which has no
# os/exec, and there is no shell or init). Purging it after the apt
# step (dpkg maintainer scripts have run by then) removes the one
# package that carried every critical/high CVE in the 0.1.4 scan, at
# the price of a half-working dpkg/apt inside the image, which a
# runtime image does not need. (Verified 2026-08-27: image boots and
# serves; scan 2C 2H -> 0C 0H.)
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates libopus0 \
 && rm -rf /var/lib/apt/lists/* \
 && dpkg --purge --force-remove-essential --force-depends perl-base \
 && rm -rf /var/lib/dpkg/info/perl-base.* \
 && ! test -e /usr/bin/perl

# Unprivileged, home-less service user (trixie-slim has useradd, not adduser).
RUN useradd --uid 65532 --no-create-home --home-dir /nonexistent \
      --shell /sbin/nologin --comment "" panaudia

COPY --from=builder /out/panaudia-server /opt/panaudia/bin/panaudia-server
# The project licence and third-party notices travel with the binary:
# the image distributes MIT/BSD/Apache-licensed code and CC BY data.
COPY --from=builder /src/LICENSE /licenses/LICENSE
COPY --from=builder /src/third-party-licences.md /licenses/THIRD-PARTY-LICENCES.md
COPY server/.env.example /opt/panaudia/.env.example

# No NET_BIND_SERVICE: the service listens on 4443 as an unprivileged
# user; a deployment that wants 443/udp maps it (-p 443:4443/udp).
USER 65532:65532
# Working directory is where a mounted .env is picked up by default.
WORKDIR /opt/panaudia
EXPOSE 4443/udp
ENTRYPOINT ["/opt/panaudia/bin/panaudia-server"]

# --- bench -------------------------------------------------------------
# The capacity ceiling harness on the runtime image. Arguments after the
# image name are test flags (-test.run, -test.timeout ...); CEILING_ORDER
# and CEILING_REVERB come in as environment. Rows go to stdout.
FROM base AS bench
COPY --from=builder /out/server.test /opt/panaudia/bin/server.test
ENTRYPOINT ["/opt/panaudia/bin/server.test", "-test.v", "-test.count=1"]
CMD ["-test.run", "TestCeilingOpus"]

# --- default target ------------------------------------------------------
FROM base AS runtime

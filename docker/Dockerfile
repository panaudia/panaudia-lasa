# syntax=docker/dockerfile:1
#
# panaudia-server image. One file for every target: the build stage
# compiles the server from this repository (build context = repo root),
# the runtime stage carries the binary and libopus and nothing else.
#
#   docker buildx build --platform linux/amd64 -f docker/Dockerfile -t panaudia/panaudia-server:dev .
#
# On linux/amd64 the GEMM hot path is static libxsmm (`-tags xsmm`, JIT
# with runtime CPU dispatch, so one image serves every x86 generation)
# and is embedded in the binary. On other architectures the pure-Go
# gonum backend is used; the output is sample-for-sample identical
# (engine/gemm mix_parity_test.go), only the speed differs.
#
# Runtime configuration is environment variables only; see
# server/.env.example (also shipped at /opt/panaudia/.env.example). A
# .env file mounted at /opt/panaudia/.env, or anywhere with
# PANAUDIA_ENV_FILE pointing at it, is loaded at startup. The server
# prints its full effective configuration when it starts.

ARG GO_VERSION=1.26.4

# --- build -------------------------------------------------------------
FROM golang:${GO_VERSION}-trixie AS builder

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      build-essential git ca-certificates pkg-config libopus-dev \
 && rm -rf /var/lib/apt/lists/*

ARG TARGETARCH
# The version the binary reports and the image is tagged with; docker/build
# derives it from the server/v* git tag on the host (.git is not in the
# build context).
ARG VERSION=dev

# libxsmm, static, pinned (x86 only). The libxsmm_dispatch_gemm API the
# engine uses exists only on the unreleased 2.0 line, hence a SHA, not a
# release; bump deliberately and re-run the GEMM parity tests. STATIC=1:
# embedded in the Go binary, no runtime library. BLAS=0: no fallback deps.
ARG LIBXSMM_REF=1d7138cac9f446284daeaf65041cc9043cec2a3b
RUN if [ "$TARGETARCH" = "amd64" ]; then \
      git clone https://github.com/libxsmm/libxsmm.git /tmp/libxsmm \
   && cd /tmp/libxsmm \
   && git checkout "${LIBXSMM_REF}" \
   && make -j"$(nproc)" STATIC=1 BLAS=0 \
   && make PREFIX=/usr/local STATIC=1 install \
   && cd / && rm -rf /tmp/libxsmm; \
    fi

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
 && TAGS="nolibopusfile"; if [ "$TARGETARCH" = "amd64" ]; then TAGS="$TAGS,xsmm"; fi \
 && go build -tags "$TAGS" -trimpath -ldflags "-X main.version=${VERSION}" -o /out/panaudia-server . \
 && if [ "$TARGETARCH" = "amd64" ]; then \
      go tool nm /out/panaudia-server | grep -q libxsmm_dispatch_gemm \
        || { echo "ERROR: amd64 binary does not embed libxsmm (built without -tags xsmm?)" >&2; exit 1; }; \
      echo "OK: static libxsmm embedded"; \
    fi

# --- runtime -----------------------------------------------------------
FROM debian:trixie-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates libopus0 \
 && rm -rf /var/lib/apt/lists/*

# Unprivileged, home-less service user (trixie-slim has useradd, not adduser).
RUN useradd --uid 65532 --no-create-home --home-dir /nonexistent \
      --shell /sbin/nologin --comment "" panaudia

COPY --from=builder /out/panaudia-server /opt/panaudia/bin/panaudia-server
# The project licence and third-party notices travel with the binary:
# the image distributes MIT/BSD/Apache-licensed code and CC BY data.
COPY --from=builder /src/LICENSE /licenses/LICENSE
COPY --from=builder /src/third-party-licences.md /licenses/THIRD-PARTY-LICENCES.md
COPY server/.env.example /opt/panaudia/.env.example

# No setcap: the service listens on 4443 as an unprivileged user; a
# deployment that wants 443/udp maps it (-p 443:4443/udp). (setcap
# would also rewrite the binary into a second 14 MB layer.)
USER 65532:65532
# Working directory is where a mounted .env is picked up by default.
WORKDIR /opt/panaudia
EXPOSE 4443/udp
ENTRYPOINT ["/opt/panaudia/bin/panaudia-server"]

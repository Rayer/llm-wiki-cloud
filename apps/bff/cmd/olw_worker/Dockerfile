FROM golang:1.26-bookworm AS build
WORKDIR /src
ARG BUILD_NONCE
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN : "${BUILD_NONCE:?BUILD_NONCE is required}" \
    && sh ./cmd/olw_worker/validate_build_nonce.sh "${BUILD_NONCE}" \
    && CGO_ENABLED=0 GOOS=linux go build -ldflags "-X main.buildNonce=${BUILD_NONCE}" -o /out/worker ./cmd/olw_worker

FROM python:3.12-slim AS worker-runtime
ARG BUILD_NONCE
ARG SYNTO_VERSION=0.7.0
ARG SYNTO_ARTIFACT_URL=https://files.pythonhosted.org/packages/4a/e9/41c6b61338d98820780a43ed075cd77525674c38242110435330771d771b/synto-0.7.0-py3-none-any.whl
ARG SYNTO_SHA256=4bc8dcf14b53f45fac32ce737ecf878f1a46d6d0b010c7decbe6c3b7b10afa77
LABEL io.llm-wiki.build.nonce="${BUILD_NONCE}"
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates git \
    && rm -rf /var/lib/apt/lists/* \
    && printf '%s' "$SYNTO_SHA256" | grep -Eq '^[0-9a-f]{64}$' \
    && printf '%s' "$SYNTO_VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' \
    && printf '%s' "$SYNTO_ARTIFACT_URL" | grep -Eq '^https://files\.pythonhosted\.org/packages/[A-Za-z0-9/._-]+\.whl$' \
    && pip install --no-cache-dir "${SYNTO_ARTIFACT_URL}#sha256=${SYNTO_SHA256}" \
    && test "$(synto --version)" = "synto, version ${SYNTO_VERSION}"

RUN git config --system user.email "worker@llm-wiki.local" \
    && git config --system user.name "LWC Worker"

COPY --from=build /out/worker /worker
WORKDIR /tmp
ENTRYPOINT ["/worker"]

FROM build AS experiment-build
ARG SOURCE_REVISION
ARG SOURCE_DIRTY
ARG BUILD_NONCE
ARG SYNTO_VERSION=0.7.0
RUN printf '%s' "$SOURCE_REVISION" | grep -Eq '^[0-9a-f]{40}$' \
    && printf '%s' "$SOURCE_DIRTY" | grep -Eq '^(true|false)$' \
    && CGO_ENABLED=0 GOOS=linux go build -ldflags "-X main.experimentRevision=${SOURCE_REVISION} -X main.experimentDirty=${SOURCE_DIRTY} -X main.experimentBuildID=${BUILD_NONCE} -X main.stagedSyntoVersion=${SYNTO_VERSION}" -o /out/query_experiment ./cmd/query_experiment

FROM worker-runtime AS experiment
COPY --from=experiment-build /out/query_experiment /query_experiment
WORKDIR /experiment
ENTRYPOINT ["/query_experiment", "staged", "--experiment-root", "/experiment", "--worker-bin", "/worker"]

# Keep the default production build and its inherited /worker ENTRYPOINT.
FROM worker-runtime AS worker

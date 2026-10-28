FROM golang:1.26-alpine AS build

ARG APP_VERSION=dev
ARG GIT_SHA=unknown
ARG GIT_BRANCH=unknown
ARG GIT_TAG=

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w \
  -X github.com/rayer/gateauth/internal/buildinfo.ProductVersion=${APP_VERSION} \
  -X github.com/rayer/gateauth/internal/buildinfo.GitSHA=${GIT_SHA} \
  -X github.com/rayer/gateauth/internal/buildinfo.GitBranch=${GIT_BRANCH} \
  -X github.com/rayer/gateauth/internal/buildinfo.GitTag=${GIT_TAG} \
  -X github.com/rayer/gateauth/internal/buildinfo.ImageTag=${GIT_SHA}" -o /auth ./cmd/auth

FROM gcr.io/distroless/static-debian12:nonroot

ARG APP_VERSION=dev
ARG GIT_SHA=unknown
ARG GIT_BRANCH=unknown
ARG GIT_TAG=

LABEL org.opencontainers.image.version=${APP_VERSION} \
      org.opencontainers.image.revision=${GIT_SHA} \
      org.opencontainers.image.ref.name=${GIT_BRANCH} \
      io.llm-wiki.git.branch=${GIT_BRANCH} \
      io.llm-wiki.git.tag=${GIT_TAG} \
      io.llm-wiki.image.tag=${GIT_SHA}

COPY --from=build /auth /auth

ENV LWC_APP_CONFIG_PATH=/config/auth.json
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/auth"]

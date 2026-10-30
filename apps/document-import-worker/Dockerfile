FROM golang:1.26-alpine AS go-build

WORKDIR /src/apps/bff
COPY apps/bff/go.mod apps/bff/go.sum ./
RUN go mod download
COPY apps/bff/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /document_import_worker ./cmd/document_import_worker

FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    DOCUMENT_IMPORT_PYTHON=/usr/local/bin/python \
    DOCUMENT_IMPORT_CONVERTER_SCRIPT=/app/convert.py

WORKDIR /app
COPY apps/document-import-worker/requirements.lock ./requirements.lock
COPY apps/document-import-worker/convert.py ./convert.py
RUN python -m pip install --no-cache-dir --require-hashes -r requirements.lock \
    && python -m markitdown --version
COPY --from=go-build /document_import_worker ./document_import_worker
USER 65532:65532
ENTRYPOINT ["/app/document_import_worker"]

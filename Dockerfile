FROM python:3.13-slim

WORKDIR /app

# System deps for google-cloud-sdk and chromadb
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl gcc g++ \
    && rm -rf /var/lib/apt/lists/*

# Install gcloud (for gsutil, gcloud auth)
RUN curl -sSL https://sdk.cloud.google.com | bash -s -- --disable-prompts --install-dir=/usr/local

# Python deps
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY worker.py watchdog.py ./

ENTRYPOINT ["python3", "worker.py"]

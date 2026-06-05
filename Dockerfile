FROM python:3.12-slim

# Install OLW (system-wide) + gcloud
RUN pip install obsidian-llm-wiki && \
    apt-get update && apt-get install -y curl apt-transport-https ca-certificates gnupg && \
    echo "deb [signed-by=/usr/share/keyrings/cloud.google.gpg] https://packages.cloud.google.com/apt cloud-sdk main" | \
    tee -a /etc/apt/sources.list.d/google-cloud-sdk.list && \
    curl https://packages.cloud.google.com/apt/doc/apt-key.gpg | \
    gpg --dearmor -o /usr/share/keyrings/cloud.google.gpg && \
    apt-get update && apt-get install -y google-cloud-cli && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /data

COPY worker.py watchdog.py /

ENTRYPOINT ["python3", "/worker.py"]

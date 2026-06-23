FROM python:3.12-slim

# SSL certs for DeepSeek API healthcheck (prevents httpx SSL failures)
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates && \
    rm -rf /var/lib/apt/lists/*

# Install OLW (no gcsfuse — CSI volume mount handles it)
RUN pip install --no-cache-dir obsidian-llm-wiki

# Copy entrypoint + JSONL regenerator + healthcheck patch
COPY entrypoint.sh /entrypoint.sh
COPY regenerate-jsonl.py /regenerate-jsonl.py
COPY healthcheck_patch.py /healthcheck_patch.py
RUN chmod +x /entrypoint.sh

ENV PYTHONSTARTUP=/healthcheck_patch.py

ENTRYPOINT ["/entrypoint.sh"]

FROM python:3.12-slim

# SSL certs for DeepSeek API
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates && \
    rm -rf /var/lib/apt/lists/*

RUN pip install --no-cache-dir obsidian-llm-wiki google-cloud-monitoring

COPY worker.py /worker.py
COPY run_pipeline.py /run_pipeline.py
COPY regenerate-jsonl.py /regenerate-jsonl.py
RUN chmod +x /worker.py

ENTRYPOINT ["python3", "/worker.py"]

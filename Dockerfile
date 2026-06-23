FROM python:3.12-slim

# Install OLW (no gcsfuse — CSI volume mount handles it)
RUN pip install --no-cache-dir obsidian-llm-wiki

# Copy entrypoint + JSONL regenerator
COPY entrypoint.sh /entrypoint.sh
COPY regenerate-jsonl.py /regenerate-jsonl.py
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]

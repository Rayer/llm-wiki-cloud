FROM debian:bookworm-slim

# Install gcsfuse + Python + pip
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl gnupg python3 python3-pip \
    && rm -rf /var/lib/apt/lists/*

# Install gcsfuse via official Google repo
RUN curl -fsSL https://packages.cloud.google.com/apt/doc/apt-key.gpg | gpg --dearmor -o /usr/share/keyrings/cloud.google.gpg \
    && echo "deb [signed-by=/usr/share/keyrings/cloud.google.gpg] https://packages.cloud.google.com/apt gcsfuse-bookworm main" > /etc/apt/sources.list.d/gcsfuse.list \
    && apt-get update && apt-get install -y --no-install-recommends gcsfuse \
    && rm -rf /var/lib/apt/lists/*

# Install OLW
RUN pip3 install --break-system-packages --no-cache-dir obsidian-llm-wiki

# Copy entrypoint
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]

FROM alpine:latest

# Install Python + pip (no gcsfuse needed — CSI mount handles it)
RUN apk add --no-cache python3 py3-pip && rm -rf /var/cache/apk/*

# Install OLW
RUN pip3 install --break-system-packages --no-cache-dir obsidian-llm-wiki

# Copy entrypoint
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]

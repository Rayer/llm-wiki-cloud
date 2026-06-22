FROM alpine:latest

# Install gcsfuse + Python + pip (all in one layer)
RUN apk add --no-cache \
    gcsfuse \
    python3 \
    py3-pip \
    && rm -rf /var/cache/apk/*

# Install OLW
RUN pip3 install --break-system-packages --no-cache-dir obsidian-llm-wiki

# Copy entrypoint
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]

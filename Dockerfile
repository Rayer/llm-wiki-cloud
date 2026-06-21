FROM gcr.io/google.com/cloudsdktool/google-cloud-cli:alpine

# Install Python + pip
RUN apk add --no-cache python3 py3-pip

# Install OLW
RUN pip3 install --no-cache-dir obsidian-llm-wiki

# Copy entrypoint
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# GCS FUSE needs /dev/fuse — privileged or --device at runtime
ENTRYPOINT ["/entrypoint.sh"]

FROM gcr.io/google.com/cloudsdktool/google-cloud-cli:alpine

# Install Python + pip + gcsfuse
RUN apk add --no-cache python3 py3-pip gcsfuse

# Install OLW
RUN pip3 install --no-cache-dir obsidian-llm-wiki

# Copy entrypoint
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]

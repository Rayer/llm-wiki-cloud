FROM scratch
COPY auth /auth
COPY ca-certificates.crt /etc/ssl/cert.pem
ENV SSL_CERT_FILE=/etc/ssl/cert.pem
ENV LWC_APP_CONFIG_PATH=/config/auth.json
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/auth"]

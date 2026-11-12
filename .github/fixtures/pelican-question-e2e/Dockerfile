# Dependencies, Chromium and a fixture-only CA are prepared before isolation.
# Build context is .github, not the application tree (whose .dockerignore excludes it).
ARG APP_IMAGE=pelican-app-base:unset
FROM mcr.microsoft.com/playwright:v1.56.1-noble AS runner
WORKDIR /opt/fixture
RUN npm install --no-save --ignore-scripts playwright@1.56.1 pg@8.16.3
RUN mkdir -p /opt/fixture/tls && \
    openssl req -x509 -newkey rsa:2048 -nodes -days 7 \
      -subj '/CN=fake-upstream' -addext 'subjectAltName=DNS:fake-upstream' \
      -addext 'basicConstraints=critical,CA:TRUE' \
      -keyout /opt/fixture/tls/key.pem -out /opt/fixture/tls/cert.pem && \
    chmod 644 /opt/fixture/tls/key.pem
COPY scripts/pelican-question-e2e.mjs /opt/fixture/pelican-question-e2e.mjs
ENV NODE_EXTRA_CA_CERTS=/opt/fixture/tls/cert.pem
ENTRYPOINT ["node", "/opt/fixture/pelican-question-e2e.mjs"]

# Only add trust for this disposable fake-upstream certificate. Keep the real
# application binary, embedded frontend, resources and normal entrypoint.
FROM ${APP_IMAGE} AS app
COPY --from=runner /opt/fixture/tls/cert.pem /fixture-ca.pem
RUN cat /fixture-ca.pem >> /etc/ssl/certs/ca-certificates.crt

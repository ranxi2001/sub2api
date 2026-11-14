FROM alpine:3.23
WORKDIR /app
RUN mkdir -p /app/data
COPY server /app/sub2api
COPY resources /app/resources
ENV DATA_DIR=/app/data
ENTRYPOINT ["/app/sub2api"]

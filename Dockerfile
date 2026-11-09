# Full build includes the mysql driver. Internet is required for go mod download.
FROM golang:1.23-alpine AS build
WORKDIR /src
# Copy go.sum as well: go mod download then verifies the pinned module hashes
# instead of silently fetching whatever the proxy serves.
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -tags mysql -trimpath -ldflags="-s -w" -o /taskforge ./cmd/server && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /mockllm ./cmd/mockllm

FROM alpine:3.20
RUN addgroup -S app && adduser -S -G app app && mkdir /data && chown app:app /data
USER app
WORKDIR /data
COPY --from=build /taskforge /usr/local/bin/taskforge
COPY --from=build /mockllm /usr/local/bin/mockllm
ENV ADDR=0.0.0.0:8080 STORAGE_BACKEND=file DATA_FILE=/data/jobs.json
EXPOSE 8080
CMD ["/usr/local/bin/taskforge"]

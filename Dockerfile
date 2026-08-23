# build stage
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.Version=${VERSION}" -o /out/stats_tracker .

# run stage
FROM alpine:3.20
RUN adduser -D -h /data -u 1000 urwebdash \
    && apk add --no-cache ca-certificates wget su-exec
COPY --from=build /out/stats_tracker /usr/local/bin/stats_tracker
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh

ENV STATS_DB=/data/wallet_stats.db \
    JWT_PATH=/data/jwt \
    PAYOUT_NOTIFY_STORE=/data/payout_notified.json \
    HOST=0.0.0.0
VOLUME /data
EXPOSE 3001

# Starts as root so the entrypoint can take ownership of a root-created bind
# mount, then drops to the unprivileged user via su-exec.
WORKDIR /data
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["stats_tracker", "run"]

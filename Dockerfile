FROM golang:1.26-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/backup-sys .

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates postgresql-client-15 \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system --gid 10001 backup \
    && useradd --system --uid 10001 --gid backup --home-dir /app backup \
    && mkdir -p /app /backups \
    && chown backup:backup /app /backups

WORKDIR /app
COPY --from=build /out/backup-sys /usr/local/bin/backup-sys

USER backup
VOLUME ["/backups"]
ENTRYPOINT ["backup-sys"]
CMD ["schedule"]

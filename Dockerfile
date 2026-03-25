FROM golang:1.26.1-alpine AS build-base

WORKDIR /src
COPY go.mod /src/
COPY cmd /src/cmd
COPY internal /src/internal
RUN go mod tidy

FROM build-base AS build-bot
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/epic-assets-notify-bot ./cmd/bot

FROM build-base AS build-migrate
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/migrate-legacy ./cmd/migrate_legacy

FROM golang:1.26.1-bookworm AS playwright-installer
ARG PLAYWRIGHT_GO_VERSION=v0.5700.1
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright
ENV PLAYWRIGHT_DRIVER_PATH=/ms-playwright-go/0.5700.1
RUN mkdir -p "$PLAYWRIGHT_DRIVER_PATH" \
    && GOBIN=/pwbin go install github.com/playwright-community/playwright-go/cmd/playwright@${PLAYWRIGHT_GO_VERSION}

FROM debian:bookworm-slim AS runtime-base
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright
ENV PLAYWRIGHT_DRIVER_PATH=/ms-playwright-go/0.5700.1
ENV MOZ_REMOTE_SETTINGS_DEVTOOLS=1
COPY --from=playwright-installer /pwbin/playwright /usr/local/bin/playwright
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && playwright install --with-deps firefox \
    && rm -f /usr/local/bin/playwright \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/* /root/.cache/* /tmp/* /var/tmp/* \
       /usr/share/doc/* /usr/share/man/* /usr/share/locale/* /usr/share/info/* \
       /usr/share/lintian/* /usr/share/linda/* /var/cache/debconf/*-old \
       /etc/apt/sources.list.d/*
WORKDIR /app
COPY locales /app/locales

FROM scratch AS migrate-legacy
COPY --from=build-migrate /out/migrate-legacy /migrate-legacy
ENTRYPOINT ["/migrate-legacy"]

FROM runtime-base AS bot
COPY --from=build-bot /out/epic-assets-notify-bot /usr/local/bin/epic-assets-notify-bot
CMD ["/usr/local/bin/epic-assets-notify-bot"]

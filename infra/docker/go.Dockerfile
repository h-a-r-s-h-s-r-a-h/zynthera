# Builds the three Go binaries once, then offers them in three ways:
#
#   --target api      the control plane image (zynthera-api)
#   --target export   only the binaries, for copying out of Docker:
#                       docker build -f infra/docker/go.Dockerfile --target export --output type=local,dest=bin .
#                     (this is how a server gets zynthera-agent and
#                     zynthera-file-bridge without having Go installed)
#
# Build context: the repository root.

FROM golang:1.27-alpine AS build
WORKDIR /src
# Dependencies first, so they are cached until go.mod changes.
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY db ./db
# Static binaries: they run on any Linux, including inside a guest image.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/zynthera-api ./cmd/api \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/zynthera-agent ./cmd/agent \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/zynthera-file-bridge ./cmd/file-bridge

FROM scratch AS export
COPY --from=build /out/ /

FROM alpine:3.22 AS api
# ca-certificates: TLS to the SMTP provider and to agents on other hosts.
# tzdata: correct timestamps in emails and logs.
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -H -u 10001 zynthera
COPY --from=build /out/zynthera-api /usr/local/bin/zynthera-api
USER zynthera
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/zynthera-api"]
CMD ["serve"]

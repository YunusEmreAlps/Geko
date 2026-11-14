FROM golang:1.26.11-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/geko ./cmd/geko \
    && mkdir -p /out/data

FROM scratch
LABEL org.opencontainers.image.source="https://github.com/YunusEmreAlps/geko" \
      org.opencontainers.image.description="Find the failure. Follow the trace." \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/geko /geko
COPY --chown=65532:65532 --from=build /out/data /data
COPY LICENSE /LICENSE
USER 65532:65532
ENV GEKO_DB_PATH=/data/geko.db
ENTRYPOINT ["/geko"]
CMD ["serve"]

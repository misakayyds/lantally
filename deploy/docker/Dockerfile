FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY . .
ARG VERSION=0.1.0-dev
ARG COMMIT=unknown
ARG DATE=unknown
ENV CGO_ENABLED=0
RUN LDFLAGS="-s -w -X github.com/misakayyds/lantally/internal/version.Version=${VERSION} -X github.com/misakayyds/lantally/internal/version.Commit=${COMMIT} -X github.com/misakayyds/lantally/internal/version.Date=${DATE}" \
 && go build -trimpath -ldflags "$LDFLAGS" -o /out/lantally-server ./cmd/lantally-server \
 && go build -trimpath -ldflags "$LDFLAGS" -o /out/lantally-allinone ./cmd/lantally-allinone \
 && mkdir -p \
      /out/agents/linux/amd64 \
      /out/agents/linux/arm64 \
      /out/agents/linux/arm \
      /out/agents/linux/mips \
      /out/agents/linux/mipsle \
      /out/agents/darwin/amd64 \
      /out/agents/darwin/arm64 \
      /out/agents/windows/amd64 \
 && GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$LDFLAGS" -o /out/agents/linux/amd64/lantally-agent ./cmd/lantally-agent \
 && GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$LDFLAGS" -o /out/agents/linux/arm64/lantally-agent ./cmd/lantally-agent \
 && GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags "$LDFLAGS" -o /out/agents/linux/arm/lantally-agent ./cmd/lantally-agent \
 && GOOS=linux GOARCH=mips GOMIPS=softfloat go build -trimpath -ldflags "$LDFLAGS" -o /out/agents/linux/mips/lantally-agent ./cmd/lantally-agent \
 && GOOS=linux GOARCH=mipsle GOMIPS=softfloat go build -trimpath -ldflags "$LDFLAGS" -o /out/agents/linux/mipsle/lantally-agent ./cmd/lantally-agent \
 && GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "$LDFLAGS" -o /out/agents/darwin/amd64/lantally-agent ./cmd/lantally-agent \
 && GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$LDFLAGS" -o /out/agents/darwin/arm64/lantally-agent ./cmd/lantally-agent \
 && GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$LDFLAGS" -o /out/agents/windows/amd64/lantally-agent.exe ./cmd/lantally-agent

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/lantally-server /usr/local/bin/lantally-server
COPY --from=build /out/lantally-allinone /usr/local/bin/lantally-allinone
COPY --from=build /out/agents /usr/share/lantally/agents
ENV LANTALLY_AGENT_DIR=/usr/share/lantally/agents
VOLUME ["/var/lib/lantally"]
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/lantally-allinone"]

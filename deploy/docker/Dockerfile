FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/lantally-server ./cmd/lantally-server \
 && CGO_ENABLED=0 go build -o /out/lantally-allinone ./cmd/lantally-allinone \
 && mkdir -p \
      /out/agents/linux/amd64 \
      /out/agents/linux/arm64 \
      /out/agents/linux/arm \
      /out/agents/darwin/amd64 \
      /out/agents/darwin/arm64 \
      /out/agents/windows/amd64 \
 && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/agents/linux/amd64/lantally-agent ./cmd/lantally-agent \
 && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /out/agents/linux/arm64/lantally-agent ./cmd/lantally-agent \
 && CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -o /out/agents/linux/arm/lantally-agent ./cmd/lantally-agent \
 && CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o /out/agents/darwin/amd64/lantally-agent ./cmd/lantally-agent \
 && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o /out/agents/darwin/arm64/lantally-agent ./cmd/lantally-agent \
 && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /out/agents/windows/amd64/lantally-agent.exe ./cmd/lantally-agent

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/lantally-server /usr/local/bin/lantally-server
COPY --from=build /out/lantally-allinone /usr/local/bin/lantally-allinone
COPY --from=build /out/agents /usr/share/lantally/agents
ENV LANTALLY_AGENT_DIR=/usr/share/lantally/agents
VOLUME ["/var/lib/lantally"]
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/lantally-allinone"]

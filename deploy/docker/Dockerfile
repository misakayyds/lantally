FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/lantally-server ./cmd/lantally-server
RUN CGO_ENABLED=0 go build -o /out/lantally-allinone ./cmd/lantally-allinone

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/lantally-server /usr/local/bin/lantally-server
COPY --from=build /out/lantally-allinone /usr/local/bin/lantally-allinone
VOLUME ["/var/lib/lantally"]
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/lantally-allinone"]

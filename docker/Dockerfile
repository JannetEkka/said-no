# Said No on a standard open-source Bifrost, built from source with dynamic
# plugin loading (Bifrost's default image is statically linked and can't load
# Go plugins; see https://docs.getbifrost.ai/plugins/building-dynamic-binary).
# The gateway and the plugin are built in one stage with the same Go toolchain,
# libc and Bifrost sources, which Go plugins require.

ARG BIFROST_COMMIT=bc27da0d433264d626f59644e8c6937b56358748

FROM node:22-bookworm AS ui
ARG BIFROST_COMMIT
WORKDIR /src
RUN git clone https://github.com/maximhq/bifrost.git bifrost && cd bifrost && git checkout "$BIFROST_COMMIT"
RUN cd bifrost/ui && npm ci --no-audit --no-fund && npx vite build && rm -rf ../transports/bifrost-http/ui && cp -r out ../transports/bifrost-http/ui

FROM golang:1.27.4-bookworm AS build
WORKDIR /src
COPY --from=ui /src/bifrost /src/bifrost
COPY plugin /src/said-no/plugin
COPY decoy /src/said-no/decoy
RUN cd bifrost && go work init ./core ./framework ./transports \
    && for d in $(find plugins cli -name go.mod -not -path '*/node_modules/*' -exec dirname {} \;); do go work use "$d"; done \
    && go work use /src/said-no/plugin
RUN cd bifrost/transports/bifrost-http && CGO_ENABLED=1 go build -trimpath -o /out/bifrost-http .
RUN cd said-no/plugin && GOWORK=/src/bifrost/go.work CGO_ENABLED=1 go build -trimpath -buildmode=plugin -o /out/said-no.so .
RUN cd said-no/decoy && GOWORK=off CGO_ENABLED=0 go build -trimpath -o /out/decoy .

FROM node:22-bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends python3 ca-certificates curl && rm -rf /var/lib/apt/lists/* \
    && npm install -g tavily-mcp@0.2.22
COPY --from=build /out/ /app/bin/
COPY bench /app/bench
COPY docker/entrypoint.sh /app/entrypoint.sh
ENV BIFROST_BIN=/app/bin/bifrost-http \
    TAVILY_MCP_COMMAND=tavily-mcp
EXPOSE 8080
ENTRYPOINT ["/app/entrypoint.sh"]

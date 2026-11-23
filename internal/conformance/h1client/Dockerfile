# Http11Probe built from a pinned commit on .NET images pinned by digest.
# The harness builds it with `docker build -` (no context), passes COMMIT
# (ProbeCommit in docker.go) and tags the image with it. Copying the
# published binaries onto the runtime image keeps the cached CI image small.
FROM mcr.microsoft.com/dotnet/sdk:10.0@sha256:fe0fd4e93bd2fbb47c883b7a6369e5c8d3db8bf01cdc5981f6ee35fbdf3ff4fb AS build
ARG COMMIT
WORKDIR /src
RUN test -n "$COMMIT" \
 && git init -q . \
 && git fetch -q --depth 1 https://github.com/MDA2AV/Http11Probe "$COMMIT" \
 && git checkout -q FETCH_HEAD
RUN dotnet publish src/Http11Probe.Cli -c Release -f net10.0 -o /probe

FROM mcr.microsoft.com/dotnet/runtime:10.0@sha256:21bf94ddcec8efebe4ffa64a6bfd30fc270ee87f54277aa08210e4a1d5a0c560
COPY --from=build /probe /probe
ENTRYPOINT ["dotnet", "/probe/Http11Probe.Cli.dll"]

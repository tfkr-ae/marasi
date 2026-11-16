# Http11Probe built from a pinned commit on a .NET SDK image pinned by
# digest. The harness builds it with `docker build -` (no context), passes
# COMMIT (ProbeCommit in docker.go) and tags the image with it.
FROM mcr.microsoft.com/dotnet/sdk:10.0@sha256:fe0fd4e93bd2fbb47c883b7a6369e5c8d3db8bf01cdc5981f6ee35fbdf3ff4fb
ARG COMMIT
WORKDIR /src
RUN test -n "$COMMIT" \
 && git init -q . \
 && git fetch -q --depth 1 https://github.com/MDA2AV/Http11Probe "$COMMIT" \
 && git checkout -q FETCH_HEAD
RUN dotnet publish src/Http11Probe.Cli -c Release -f net10.0 -o /probe
ENTRYPOINT ["dotnet", "/probe/Http11Probe.Cli.dll"]

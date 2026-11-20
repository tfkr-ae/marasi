# grpc-java's interop server and client, io.grpc:grpc-interop-testing from
# Maven Central at a pinned version, on images pinned by digest. The harness
# builds it with `docker build -` (no context), passes VERSION
# (InteropVersion in docker.go) and tags the image with it. The jars and
# their dependencies go to /interop/lib; a run puts its own certificates
# ahead of them on the classpath.
FROM maven:3.9.11-eclipse-temurin-21@sha256:6fdc855a6ed81d288ca7ca37ac6ff5e9308b612485c0801d70b25a858c83d237 AS fetch
ARG VERSION
WORKDIR /fetch
RUN test -n "$VERSION" \
 && printf '%s\n' \
    '<project xmlns="http://maven.apache.org/POM/4.0.0">' \
    '  <modelVersion>4.0.0</modelVersion>' \
    '  <groupId>marasi.conformance</groupId><artifactId>grpc-interop</artifactId><version>1</version>' \
    '  <dependencies><dependency>' \
    "    <groupId>io.grpc</groupId><artifactId>grpc-interop-testing</artifactId><version>$VERSION</version>" \
    '  </dependency></dependencies>' \
    '</project>' > pom.xml \
 && mvn -q -B dependency:copy-dependencies -DincludeScope=runtime -DoutputDirectory=/interop/lib

FROM eclipse-temurin:21-jre@sha256:ca73b4d3961703d6d7830a4361f49ef0478f8f28b39defc07bb72f4cc7f834f6
COPY --from=fetch /interop/lib /interop/lib

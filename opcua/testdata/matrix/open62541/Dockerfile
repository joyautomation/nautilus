# open62541 fixture for the OPC UA client matrix (opcua/matrix_test.go):
# matrix_server.c built against a tagged open62541 release. The images on
# Docker Hub stop at a 2021 master build, and an embedded device ships a
# release, so the matrix builds one. Bump OPEN62541 deliberately. The base
# is pinned by digest (bookworm-slim, 2026-11), so a rebuild is the same.
FROM debian:bookworm-slim@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc AS build
ARG OPEN62541=v1.4.22
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates cmake gcc git libc6-dev libssl-dev make python3 \
 && rm -rf /var/lib/apt/lists/*
RUN git clone --depth 1 --branch ${OPEN62541} --recurse-submodules --shallow-submodules \
      https://github.com/open62541/open62541.git /src
RUN cmake -S /src -B /build -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr/local \
      -DUA_ENABLE_ENCRYPTION=OPENSSL -DUA_NAMESPACE_ZERO=FULL \
      -DUA_ENABLE_SUBSCRIPTIONS=ON -DUA_BUILD_EXAMPLES=OFF -DUA_BUILD_UNIT_TESTS=OFF \
 && cmake --build /build -j"$(nproc)" && cmake --install /build
COPY matrix_server.c /matrix_server.c
RUN gcc -O2 -Wall -Werror -o /matrix_server /matrix_server.c -lopen62541 -lssl -lcrypto -lpthread -lm

FROM debian:bookworm-slim@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc
RUN apt-get update && apt-get install -y --no-install-recommends libssl3 \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /matrix_server /usr/local/bin/matrix_server
EXPOSE 4840
ENTRYPOINT ["/usr/local/bin/matrix_server"]

# The controller image for water-scada — adapted from `naut new`'s
# cmd/naut/templates/Dockerfile.tmpl (documented here, not built or run:
# see ../README.md). `naut build` produces a self-contained binary
# (runtime + this project, one file), so the image is just that file on a
# distroless base — no Go toolchain in the image either.
#
#   naut build -o water-scada .
#   docker build -t water-scada -f deploy/Dockerfile .
#
# distroless/static rather than scratch: MQTT/TLS needs CA roots, but
# nothing here needs a shell. The base has no libc, so the binary must be
# STATIC — a released binary already is; `go build` yourself needs
# CGO_ENABLED=0.
FROM gcr.io/distroless/static-debian12:nonroot
COPY water-scada /controller
# The tag API, dashboard, and /api/alarms, /api/cluster; NAUTILUS_ADDR overrides.
ENV NAUTILUS_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["/controller"]

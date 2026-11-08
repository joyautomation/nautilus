# The playground's saved projects (see main.go). Build context is the repo
# root: docker build -f playground/share/Dockerfile .
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY playground/share ./playground/share
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -o /share ./playground/share

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /share /share
# The volume the projects are kept on (k8s.yml mounts it).
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/share", "-addr", ":8080", "-data", "/data"]

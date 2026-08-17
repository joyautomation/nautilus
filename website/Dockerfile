# Build from the REPO ROOT so ../docs is in the build context:
#   docker build -f website/Dockerfile -t nautilus-docs .
FROM node:22-alpine AS build
WORKDIR /repo
COPY docs ./docs
COPY hmi/README.md ./hmi/README.md
COPY website ./website
WORKDIR /repo/website
RUN npm ci && npm run build

FROM nginx:alpine
COPY --from=build /repo/website/dist /usr/share/nginx/html

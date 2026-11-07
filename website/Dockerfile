FROM node:22-alpine AS build
WORKDIR /repo
COPY docs ./docs
COPY hmi/README.md ./hmi/README.md
COPY tools/vscode-iec/syntaxes ./tools/vscode-iec/syntaxes
# The playground, built on the runner before `docker build` (docs.yml runs
# playground/scripts/build.sh); scripts/sync-playground.mjs copies it into
# public/playground/engine/.
COPY playground/dist ./playground/dist
COPY website ./website
WORKDIR /repo/website
RUN npm ci && npm run build

FROM nginx:alpine
COPY website/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /repo/website/dist /usr/share/nginx/html
# The playground's build id, for nginx.conf's long-lived ?v= responses.
RUN b=$(sed -n 's/.*"build":"\([0-9a-z]*\)".*/\1/p' /usr/share/nginx/html/playground/engine/engine.json 2>/dev/null) \
 && sed -i "s/@PLAYGROUND_BUILD@/${b:-unbuilt}/" /etc/nginx/conf.d/default.conf
# A .gz beside every compressible file, for nginx.conf's gzip_static.
RUN find /usr/share/nginx/html -type f -size +1k \( -name '*.html' -o -name '*.css' -o -name '*.js' \
      -o -name '*.mjs' -o -name '*.json' -o -name '*.svg' -o -name '*.wasm' -o -name '*.map' \
      -o -name '*.txt' -o -name '*.xml' -o -name '*.ttf' \) \
      -exec sh -c 'for f; do gzip -9 -c "$f" > "$f.gz"; done' _ {} +

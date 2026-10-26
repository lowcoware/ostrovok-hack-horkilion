# Documentation site: renders docs/ with Docusaurus and serves the static
# files. Build from the repository root, docs/ and docs-site/ are both needed:
#   docker build -f docs-site/Dockerfile .
FROM node:24-alpine AS build
WORKDIR /repo/docs-site
COPY docs-site/package.json docs-site/package-lock.json ./
RUN npm ci
COPY docs /repo/docs
COPY docs-site ./
RUN npm run build

# Only the Caddy binary is taken from the official image; the runtime is empty.
FROM caddy:2.10-alpine AS caddy
RUN mkdir -p /rootfs/tmp && chmod 1777 /rootfs/tmp

FROM scratch
COPY --from=caddy /usr/bin/caddy /usr/bin/caddy
COPY --from=caddy /rootfs/ /
# The site's baseUrl is /docs/ and the web Caddy proxies /docs/* here with the
# path intact, so the files live under /srv/docs.
COPY --from=build /repo/docs-site/build /srv/docs
ENV HOME=/tmp XDG_CONFIG_HOME=/tmp XDG_DATA_HOME=/tmp
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/bin/caddy", "file-server", "--root", "/srv", "--listen", ":8080"]

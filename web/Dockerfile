# Builds the frontend and serves it with Caddy, which also terminates TLS and
# proxies /api, Langfuse and Grafana. The Caddyfile is mounted from deploy/caddy.
FROM node:24-alpine AS build
WORKDIR /web
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

# Only the Caddy binary is taken from the official image; the runtime is empty.
FROM caddy:2.10-alpine AS caddy
RUN mkdir -p /rootfs/tmp /rootfs/data /rootfs/config /rootfs/etc/caddy && chmod 1777 /rootfs/tmp

FROM scratch
COPY --from=caddy /usr/bin/caddy /usr/bin/caddy
COPY --from=caddy /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=caddy /rootfs/ /
COPY --from=build /web/dist /srv
ENV XDG_CONFIG_HOME=/config XDG_DATA_HOME=/data
EXPOSE 80 443 443/udp
# Root inside the container, as in the official image, to bind ports 80 and 443.
ENTRYPOINT ["/usr/bin/caddy", "run", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"]

FROM node:22-alpine AS build

WORKDIR /app

ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0
RUN corepack enable

COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY web/package.json web/
COPY sdks/js/package.json sdks/js/
RUN pnpm install --frozen-lockfile --filter @spanbarn/web...

COPY web/ web/
RUN pnpm --filter @spanbarn/web build

FROM caddy:2.8-alpine

COPY --from=build /app/web/dist /srv
COPY deploy/docker/Caddyfile /etc/caddy/Caddyfile

EXPOSE 8080

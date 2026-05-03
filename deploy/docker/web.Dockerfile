FROM node:22-alpine AS build

WORKDIR /app

COPY web/package*.json ./
RUN npm ci

COPY web/ .
RUN npm run build

FROM caddy:2.8-alpine

COPY --from=build /app/dist /srv
COPY deploy/docker/Caddyfile /etc/caddy/Caddyfile

EXPOSE 8080

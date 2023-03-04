FROM caddy:2
ADD caddy /usr/bin/caddy
ADD example.caddyfile /etc/caddy/Caddyfile

EXPOSE 2023

LABEL org.opencontainers.image.source="https://github.com/dulli/caddy-wol"
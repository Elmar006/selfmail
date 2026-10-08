FROM debian:trixie-slim@sha256:a29215f6a35e51e22adffa17f89e9d2ef06214e64a2bad10d765c46aea49f11f
ENV DEBIAN_FRONTEND=noninteractive
ARG POSTFIX_VERSION=3.10.13-0+deb13u1
RUN apt-get update && echo 'postfix postfix/main_mailer_type select Internet Site' | debconf-set-selections && apt-get install -y --no-install-recommends postfix=${POSTFIX_VERSION} ca-certificates && rm -rf /var/lib/apt/lists/*
COPY entrypoint.sh /usr/local/bin/mail-entrypoint
COPY rotate-loop.sh /usr/local/bin/rotate-loop
RUN chmod +x /usr/local/bin/mail-entrypoint
RUN chmod +x /usr/local/bin/rotate-loop
HEALTHCHECK --interval=10s --timeout=3s --start-period=20s CMD postfix status || exit 1
EXPOSE 25
ENTRYPOINT ["/usr/local/bin/mail-entrypoint"]

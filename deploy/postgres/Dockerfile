FROM golang:1.26-bookworm@sha256:dc9ad6c05acc7a88e5b71bde60a5fe3bd4b9f0db209011711b464107438a8107 AS gosu-build
RUN git clone https://github.com/tianon/gosu.git /gosu && cd /gosu \
 && git checkout 6456aaa0f3c854d199d0f037f068eb97515b7513 \
 && go mod download && go mod verify \
 && CGO_ENABLED=0 go build -mod=readonly -trimpath -o /out/gosu .

FROM postgres:18-bookworm@sha256:afc7e2d441324c0388fa80c3d24f733b4194a4eb7f47dd8ee2b08eb1a24a647c
COPY --from=gosu-build /out/gosu /usr/local/bin/gosu
ARG PGBACKREST_VERSION=2.59.3-1.pgdg12+1
RUN apt-get update && apt-get install -y --no-install-recommends pgbackrest=${PGBACKREST_VERSION} openssh-client && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /var/lib/pgbackrest /var/log/pgbackrest /etc/pgbackrest \
 && chown postgres:postgres /var/lib/pgbackrest /var/log/pgbackrest
COPY pgbackrest.conf /etc/pgbackrest/pgbackrest.conf
COPY pgbackrest-env.sh /usr/local/bin/selfmail-pgbackrest
COPY backup-loop.sh /usr/local/bin/selfmail-backup-loop
RUN chmod 755 /usr/local/bin/selfmail-pgbackrest /usr/local/bin/selfmail-backup-loop

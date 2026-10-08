#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mkdir -p secrets
if [ ! -s secrets/backup_passphrase ]; then (umask 077; openssl rand -hex 32 > secrets/backup_passphrase); fi
if [ ! -s secrets/evidence_passphrase ]; then (umask 077; openssl rand -hex 32 > secrets/evidence_passphrase); fi
if [ -f .env ]; then printf 'Existing .env preserved\n'; exit 0; fi
umask 077
cp .env.example .env
for key in POSTGRES_PASSWORD APP_DB_PASSWORD WORKER_DB_PASSWORD RABBITMQ_PASSWORD REDIS_PASSWORD; do
 secret=$(openssl rand -hex 32)
 sed -i "s/^$key=replace-with-random-value$/$key=$secret/" .env
done
master=$(openssl rand -base64 32)
sed -i "s|^MASTER_KEY=replace-with-base64-32-byte-key$|MASTER_KEY=$master|" .env
mkdir -p secrets
printf 'Local configuration created\n'

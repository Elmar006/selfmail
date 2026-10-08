#!/bin/sh
set -eu
: "${SMTP_HOSTNAME:?required}"
: "${BOUNCE_DOMAIN:?required}"
: "${TRUSTED_WORKERS:?required}"
case "$SMTP_HOSTNAME:$BOUNCE_DOMAIN" in *[!a-z0-9.:-]*) echo 'invalid mail hostname' >&2; exit 1 ;; esac
mkdir -p /var/log/postfix
chmod 755 /var/log/postfix
touch /var/log/postfix/mail.log
chmod 644 /var/log/postfix/mail.log
postconf -e "myhostname = $SMTP_HOSTNAME"
postconf -e 'mydestination ='
postconf -e "mynetworks = 127.0.0.0/8, $TRUSTED_WORKERS"
postconf -e "relay_domains = $BOUNCE_DOMAIN"
postconf -e 'inet_interfaces = all'
postconf -e 'inet_protocols = ipv4'
postconf -e 'smtpd_relay_restrictions = permit_mynetworks, reject_unauth_destination'
postconf -e 'smtpd_sender_restrictions = permit_mynetworks, check_sender_access regexp:/etc/postfix/null_sender'
postconf -e 'smtpd_recipient_restrictions = reject_non_fqdn_recipient, reject_unlisted_recipient'
postconf -e 'smtpd_client_connection_rate_limit = 30'
postconf -e 'disable_vrfy_command = yes'
postconf -e 'enable_long_queue_ids = yes'
postconf -e 'message_size_limit = 10485760'
postconf -e 'smtpd_helo_required = yes'
postconf -e 'smtp_tls_security_level = may'
postconf -e 'smtp_tls_CAfile = /etc/ssl/certs/ca-certificates.crt'
postconf -e 'smtp_tls_loglevel = 1'
postconf -e 'smtp_destination_concurrency_limit = 5'
postconf -e 'smtp_destination_rate_delay = 1s'
postconf -e 'default_destination_recipient_limit = 1'
postconf -e 'minimal_backoff_time = 60s'
postconf -e 'maximal_backoff_time = 1h'
postconf -e 'maximal_queue_lifetime = 2d'
postconf -e 'bounce_queue_lifetime = 1d'
postconf -e 'maillog_file = /var/log/postfix/mail.log'
postconf -e 'maillog_file_permissions = 0644'
# Postfix rejects an empty compressor command. A no-op preserves the rotated
# file until the reconciler commits its cursor; rotate-loop compresses it later.
postconf -e 'maillog_file_compressor = /bin/true'
postconf -e 'smtp_destination_recipient_limit = 50'
postconf -e 'transport_maps = regexp:/etc/postfix/transport'
postconf -e 'relay_recipient_maps = regexp:/etc/postfix/bounce_recipients'
escaped_domain=$(printf '%s' "$BOUNCE_DOMAIN" | sed 's/\./\\./g')
printf '/@%s$/ smtp:[bounce]:2526\n' "$escaped_domain" > /etc/postfix/transport
printf '/^b\+[a-f0-9]{32}\+[a-f0-9]{20}@%s$/ OK\n' "$escaped_domain" > /etc/postfix/bounce_recipients
printf '/^<>$/ OK\n/.*/ REJECT Only null-sender DSN is accepted from untrusted clients\n' > /etc/postfix/null_sender
if [ "${APP_ENV:-development}" = production ]; then
 case "$SMTP_HOSTNAME:$BOUNCE_DOMAIN" in *example.test*|*localhost*|*example.com*) echo 'production requires real mail and bounce domains' >&2; exit 1 ;; esac
 postconf -e 'relayhost ='
else
 # Local sink is an intentional safety boundary for development.
 postconf -e 'relayhost = [sink]:1025'
fi
if [ -n "${POSTFIX_TLS_CERT:-}" ] && [ -n "${POSTFIX_TLS_KEY:-}" ]; then
 postconf -e "smtpd_tls_cert_file = $POSTFIX_TLS_CERT"
 postconf -e "smtpd_tls_key_file = $POSTFIX_TLS_KEY"
 postconf -e 'smtpd_tls_security_level = may'
fi
postfix check
/usr/local/bin/rotate-loop &
exec postfix start-fg

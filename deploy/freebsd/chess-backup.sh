#!/bin/sh
# Nightly logical backup of the chess database. setup-jail.sh installs this as
# /usr/local/sbin/chess-backup and schedules it as the postgres user through
# /usr/local/etc/cron.d/chess-backup.
#
#   CHESS_BACKUP_DIR   destination (default: /var/db/postgres/backups)
#   CHESS_BACKUP_KEEP  days of dumps to keep (default: 14)
#
# Restore into a freshly provisioned database with:
#   su -m postgres -c 'pg_restore --no-owner --role=chess -d chess <dump>'
# (use --role=chess_owner for the split-privilege setup).
set -eu
PATH=/sbin:/bin:/usr/sbin:/usr/bin:/usr/local/sbin:/usr/local/bin

dir=${CHESS_BACKUP_DIR:-/var/db/postgres/backups}
keep=${CHESS_BACKUP_KEEP:-14}
umask 077
mkdir -p "$dir"

target="$dir/chess-$(date -u +%Y%m%dT%H%M%SZ).dump"
pg_dump --format=custom --dbname=chess --file="$target.partial"
mv "$target.partial" "$target"
find "$dir" -name 'chess-*.dump' -type f -mtime "+$keep" -delete

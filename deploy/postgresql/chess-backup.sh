#!/bin/sh
# Nightly logical backup of the chess database, run as the postgres account.
# Installed as /usr/local/sbin/chess-backup by deploy/freebsd/setup-jail.sh
# (scheduled through /usr/local/etc/cron.d/chess-backup) and by
# deploy/linux/setup.sh (scheduled by the chess-backup.timer systemd unit).
#
#   CHESS_BACKUP_DIR   destination (default: backups/ in the postgres home,
#                      e.g. /var/db/postgres/backups on FreeBSD)
#   CHESS_BACKUP_KEEP  days of dumps to keep (default: 14)
#
# Restore into a freshly provisioned database as the postgres account:
#   pg_restore --no-owner --role=chess -d chess <dump>
# (use --role=chess_owner for the split-privilege setup).
set -eu
PATH=/sbin:/bin:/usr/sbin:/usr/bin:/usr/local/sbin:/usr/local/bin

dir=${CHESS_BACKUP_DIR:-${HOME:?}/backups}
keep=${CHESS_BACKUP_KEEP:-14}
umask 077
mkdir -p "$dir"

target="$dir/chess-$(date -u +%Y%m%dT%H%M%SZ).dump"
if ! pg_dump --format=custom --dbname=chess --file="$target.partial"; then
	rm -f "$target.partial"
	exit 1
fi
mv "$target.partial" "$target"
find "$dir" -name 'chess-*.dump' -type f -mtime "+$keep" -delete

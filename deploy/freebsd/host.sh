#!/bin/sh
# Host-side preparation for the chess jail. Run as root on the Bastille host:
#
#   sh deploy/freebsd/host.sh <jail-name>
#
# PostgreSQL allocates a small System V shared-memory segment. This gives the
# jail its own SysV shared-memory namespace (isolated, unlike the older
# allow.sysvipc) and restarts the jail so the parameter takes effect. The
# restart stops every service in the jail, including a running chess-server.
#
# After setup-jail.sh has run inside the jail, add the location block from
# deploy/freebsd/nginx-chess.conf to the host nginx configuration, then
#   nginx -t && service nginx reload
set -eu
PATH=/sbin:/bin:/usr/sbin:/usr/bin:/usr/local/sbin:/usr/local/bin

[ $# -eq 1 ] || { echo "usage: $0 <jail-name>" >&2; exit 2; }
jail=$1
[ "$(id -u)" -eq 0 ] || { echo "host.sh: run as root" >&2; exit 1; }
command -v bastille >/dev/null 2>&1 || { echo "host.sh: bastille not found" >&2; exit 1; }

bastille config "$jail" set sysvshm new
bastille restart "$jail"
echo "host.sh: $jail now has sysvshm=new; run deploy/freebsd/setup-jail.sh inside it"

#!/bin/sh
set -eu
/usr/local/bin/refresh-upstreams.sh &
exec nginx -g 'daemon off;'

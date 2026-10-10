#!/bin/sh
# Marca down a réplica cujo /readyz não é 200 e recarrega o nginx se o pool mudou.
# 503 de uma busca (pool de Dijkstra) não passa por aqui: o proxy_next_upstream
# tenta a outra réplica sem tirar esta do pool.

dir=/etc/nginx/upstreams
pid=/var/run/nginx.pid

server_line() {
  local name=$1
  if wget -q -T 2 -O /dev/null "http://${name}:8080/readyz" 2>/dev/null; then
    echo "    server ${name}:8080;"
  else
    echo "    server ${name}:8080 down;"
  fi
}

write_block() {
  local upstream=$1
  shift
  local tmp dest
  tmp=$(mktemp "$dir/.tmp.XXXXXX") || return 1
  {
    echo "upstream ${upstream} {"
    for name in "$@"; do
      server_line "$name"
    done
    echo "}"
  } > "$tmp" || return 1

  dest="$dir/${upstream}.conf"
  if cmp -s "$tmp" "$dest"; then
    rm -f "$tmp"
    return 0
  fi
  mv "$tmp" "$dest" || return 1
  changed=1
}

refresh() {
  changed=0
  write_block ride ride-service || return 1
  write_block driver driver-service || return 1
  write_block routing routing-1 routing-2 || return 1
  if [ "$changed" -eq 1 ]; then
    nginx -s reload
  fi
}

while [ ! -s "$pid" ]; do
  sleep 1
done

while true; do
  refresh || echo "refresh upstreams failed" >&2
  sleep 2
done

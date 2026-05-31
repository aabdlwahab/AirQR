#!/usr/bin/env bash
set -euo pipefail

ca_key="airqr-ca.key"
ca_cert="airqr-ca.pem"
server_key="key.pem"
server_csr="airqr-server.csr"
server_cert="cert.pem"
server_ext="airqr-server.ext"

tmp_ips="$(mktemp)"
trap 'rm -f "$tmp_ips"' EXIT

{
  echo "127.0.0.1"
  if command -v ip >/dev/null 2>&1; then
    ip -4 -o addr show scope global | awk '{ split($4, parts, "/"); print parts[1] }'
  fi
  for ip_addr in "$@"; do
    echo "$ip_addr"
  done
} | awk 'NF && !seen[$0]++' > "$tmp_ips"

umask 077

openssl genrsa -out "$ca_key" 4096
openssl req -x509 -new -nodes -key "$ca_key" -sha256 -days 3650 \
  -subj "/CN=AirQR Local CA" \
  -out "$ca_cert"

openssl genrsa -out "$server_key" 2048
openssl req -new -key "$server_key" \
  -subj "/CN=airqr.local" \
  -out "$server_csr"

cat > "$server_ext" <<EOF
authorityKeyIdentifier=keyid,issuer
basicConstraints=CA:FALSE
keyUsage = digitalSignature, nonRepudiation, keyEncipherment, dataEncipherment
extendedKeyUsage = serverAuth
subjectAltName = @alt_names

[alt_names]
DNS.1 = localhost
DNS.2 = airqr.local
EOF

count=1
while IFS= read -r ip_addr; do
  printf 'IP.%d = %s\n' "$count" "$ip_addr" >> "$server_ext"
  count=$((count + 1))
done < "$tmp_ips"

openssl x509 -req -in "$server_csr" \
  -CA "$ca_cert" -CAkey "$ca_key" -CAcreateserial \
  -out "$server_cert" -days 825 -sha256 -extfile "$server_ext"

chmod 600 "$ca_key" "$server_key"
chmod 644 "$ca_cert" "$server_cert"

echo "Created:"
echo "  $ca_cert  install this CA certificate on the phone"
echo "  $server_cert"
echo "  $server_key"
echo
openssl x509 -in "$server_cert" -noout -subject -issuer -ext subjectAltName

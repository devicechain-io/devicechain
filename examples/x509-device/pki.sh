#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Mints a throwaway CA and one device certificate into ./pki, once. Re-running keeps them.
#
#   DEVICE_CN  the device's common name, which becomes its external id (default SN-000123)
#
# Only ca.crt and device.crt ever go to the platform. ca.key and device.key stay in ./pki.
set -euo pipefail
cd "$(dirname "$0")"
command -v openssl >/dev/null || { echo "error: openssl is required" >&2; exit 1; }
CN="${DEVICE_CN:-SN-000123}"
mkdir -p pki
cd pki
if [ -f device.crt ]; then
  echo "pki/ exists; keeping it"
  exit 0
fi
umask 077
# Git Bash on Windows would rewrite a leading "/CN=" into a path; harmless elsewhere.
export MSYS_NO_PATHCONV=1

# A CA: a P-256 key and a self-signed certificate that may sign certificates.
openssl ecparam -name prime256v1 -genkey -noout -out ca.key
openssl req -x509 -new -key ca.key -sha256 -days 3650 -subj "/CN=Example Device CA" \
  -addext "basicConstraints=critical,CA:TRUE" \
  -addext "keyUsage=critical,keyCertSign,cRLSign" \
  -out ca.crt

# A device: its own key, and a certificate the CA signs for it.
# The certificate must allow client authentication and must not be a CA.
openssl ecparam -name prime256v1 -genkey -noout -out device.key
openssl req -new -key device.key -subj "/CN=$CN" -out device.csr
cat > device.ext <<'EXT'
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=clientAuth
EXT
openssl x509 -req -in device.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -sha256 -days 90 -extfile device.ext -out device.crt 2>/dev/null
rm -f device.csr device.ext ca.srl

openssl x509 -in device.crt -noout -subject -ext extendedKeyUsage -enddate
openssl verify -CAfile ca.crt -purpose sslclient device.crt
echo "wrote pki/ (CN=$CN)"

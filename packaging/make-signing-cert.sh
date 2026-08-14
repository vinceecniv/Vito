#!/usr/bin/env bash
# Create the self-signed code-signing identity that build-macos.sh signs with.
# Run once per machine; build-macos.sh picks it up automatically afterwards.
#
# Why bother, when this is not a Developer ID and Gatekeeper still rejects the
# result: it is the difference between permissions that survive an update and
# permissions that do not. macOS stores an Accessibility or microphone approval
# as a *code signing requirement*, and what that requirement says depends
# entirely on how the app was signed:
#
#   ad-hoc       designated => cdhash H"d21db1af…"
#   this cert    designated => identifier io.github.vinceecniv.vito
#                              and certificate root = H"70355e80…"
#
# The ad-hoc form names one exact binary, so every rebuild invalidates it — the
# switch in System Settings stays on while pointing at something that no longer
# exists, and the user has to remove and re-add Vito after every update. The
# certificate form names the app and the certificate, so it keeps matching as
# long as the same certificate signs the build.
#
# What it does NOT fix: Gatekeeper. The app is still not notarised, so the first
# launch still needs Open Anyway and Finder still shows the ⌛. Only an Apple
# Developer ID plus notarytool changes that.
#
# The identity is personal and must be kept: sign every release with the same
# one, or users' permissions break exactly as they would have anyway. A backup
# copy is written to ~/.vito-signing (chmod 600). Never commit it — anyone
# holding it can sign software as you.
set -euo pipefail

CN="${VITO_SIGN_IDENTITY:-Vito Self-Signed}"
OUT="$HOME/.vito-signing"
KEYCHAIN="$HOME/Library/Keychains/login.keychain-db"

# allow_codesign opens the key's partition list to codesign.
#
# This is separate from the access list that `security import -T` sets, and both
# are needed: with the access list alone macOS still puts up a keychain password
# prompt on every single build. There is no way to set it without the keychain
# password, so it is asked for here rather than guessed at.
allow_codesign() {
  echo
  echo "To sign without a password prompt on every build, macOS needs codesign"
  echo "added to the key's partition list. That requires your login keychain"
  echo "password (the one you log in with). Press Return to skip."
  printf 'Keychain password: '
  read -rs kcpw
  echo
  if [ -z "$kcpw" ]; then
    echo "    Skipped — expect a password prompt on each build."
    return
  fi
  if security set-key-partition-list -S apple-tool:,apple:,codesign: \
       -s -k "$kcpw" "$KEYCHAIN" >/dev/null 2>&1; then
    echo "    Done — builds will no longer ask."
  else
    echo "    Failed (wrong password?). Re-run this script to try again."
  fi
  unset kcpw
}

if security find-certificate -c "$CN" >/dev/null 2>&1; then
  echo "==> '$CN' already exists in the login keychain."
  echo "    Delete it in Keychain Access first if you want to replace it."
  allow_codesign
  exit 0
fi

echo "==> creating '$CN'"
mkdir -p "$OUT"
chmod 700 "$OUT"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# codeSigning EKU plus CA:false is what makes codesign accept the certificate;
# a plain self-signed TLS certificate is rejected.
cat > "$TMP/cert.cnf" <<EOF
[ req ]
distinguished_name = dn
x509_extensions    = v3
prompt             = no

[ dn ]
CN = $CN
O  = Vito

[ v3 ]
basicConstraints     = critical,CA:false
keyUsage             = critical,digitalSignature
extendedKeyUsage     = critical,codeSigning
subjectKeyIdentifier = hash
EOF

openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
  -keyout "$TMP/key.pem" -out "$TMP/cert.pem" -config "$TMP/cert.cnf" 2>/dev/null

# -T /usr/bin/codesign puts codesign on the key's access list, so signing does
# not pop a keychain prompt on every build.
openssl pkcs12 -export -out "$TMP/identity.p12" -inkey "$TMP/key.pem" \
  -in "$TMP/cert.pem" -name "$CN" -passout pass:vito 2>/dev/null
security import "$TMP/identity.p12" -k "$KEYCHAIN" -P vito -T /usr/bin/codesign >/dev/null

cp "$TMP/identity.p12" "$OUT/identity.p12"
cp "$TMP/cert.pem" "$OUT/cert.pem"
chmod 600 "$OUT/identity.p12" "$OUT/cert.pem"

allow_codesign

echo
echo "==> done."
echo "    Identity : $CN (login keychain)"
echo "    Backup   : $OUT/identity.p12  (password: vito)"
echo
echo "    That p12 password is only needed to import this identity somewhere"
echo "    else — another Mac, or a CI runner. Back the file up, and never"
echo "    commit it: whoever holds it can sign software as you."
echo
echo "    Note: 'security find-identity -v -p codesigning' reports 0 valid"
echo "    identities, because the certificate carries no trust setting. That is"
echo "    fine — codesign signs with it regardless, and adding trust would only"
echo "    tell this Mac to believe its own signature."

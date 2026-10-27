#!/usr/bin/env bash
# Publica una versión nueva de Hilvan:  ./scripts/publicar.sh 0.2.0
#
#  1. Fija la versión, crea la etiqueta vX.Y.Z y la sube: GitHub compila Linux, Windows y macOS
#     y deja una versión BORRADOR.
#  2. Compila el APK de Android acá (firmado con tu clave, que nunca sale de esta computadora).
#  3. Espera a GitHub, baja todo, arma SHA256SUMS y lo FIRMA con tu clave de actualizaciones.
#  4. Sube el APK y la firma y publica la versión: recién ahí la ven los actualizadores.
#
# Claves: carpeta ../hilvan-firma (o HILVAN_FIRMA). Hacé copia de seguridad: sin ellas no se
# puede publicar una actualización que Hilvan (y Android) acepten.
set -euo pipefail
cd "$(dirname "$0")/.."
V="${1:?Uso: scripts/publicar.sh X.Y.Z}"; V="${V#v}"
[[ "$V" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "Versión inválida: $V" >&2; exit 1; }
FIRMA="${HILVAN_FIRMA:-$(cd .. && pwd)/hilvan-firma}"
KEY="$FIRMA/actualizaciones.key"
[ -f "$KEY" ] && [ -f "$FIRMA/hilvan.keystore" ] || { echo "Faltan las claves en $FIRMA" >&2; exit 1; }
if command -v gh >/dev/null; then GH=(gh); else GH=(nix run nixpkgs#gh --); fi
if command -v go >/dev/null; then GO=(go); else GO=(nix develop -c go); fi
PUB="$(sed -n 's/^\tupdatePublicKey = "\(.*\)"$/\1/p' internal/core/update.go)"
[ -n "$PUB" ] || { echo "No encuentro la clave pública en internal/core/update.go" >&2; exit 1; }
git diff --quiet && git diff --cached --quiet || { echo "Hay cambios sin guardar (commit) en el repositorio." >&2; exit 1; }

echo "== 1/4 Versión $V"
echo "$V" > VERSION
sed -i "s/android:versionName=\"[^\"]*\"/android:versionName=\"$V\"/" android/AndroidManifest.xml
if ! git diff --quiet; then
  git commit -qam "Hilvan $V"
fi
git tag -a "v$V" -m "Hilvan $V"
git push -q origin HEAD "v$V"

echo "== 2/4 APK de Android (firmado acá)"
HILVAN_VERSION="$V" nix develop .#android -c android/build.sh
APK="android/dist/hilvan-$V.apk"

echo "== 3/4 Esperando a GitHub (Linux, Windows, macOS)…"
sleep 15
RUN="$("${GH[@]}" run list --workflow release.yml --branch "v$V" -L 1 --json databaseId -q '.[0].databaseId')"
"${GH[@]}" run watch "$RUN" --exit-status
D="$(mktemp -d)"
"${GH[@]}" release download "v$V" -D "$D"
cp "$APK" "$D/"
(cd "$D" && sha256sum hilvan* | sort -k2 > SHA256SUMS)
"${GO[@]}" run ./cmd/hilvan-firmar firmar "$KEY" "$D/SHA256SUMS"
"${GO[@]}" run ./cmd/hilvan-firmar verificar "$PUB" "$D/SHA256SUMS"

echo "== 4/4 Publicando"
"${GH[@]}" release upload "v$V" "$D/$(basename "$APK")" "$D/SHA256SUMS" "$D/SHA256SUMS.sig" --clobber
"${GH[@]}" release edit "v$V" --draft=false --latest
rm -rf "$D"
echo "Listo: https://github.com/$("${GH[@]}" repo view --json nameWithOwner -q .nameWithOwner)/releases/tag/v$V"

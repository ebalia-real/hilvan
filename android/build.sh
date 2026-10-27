#!/usr/bin/env bash
# Compila Hilvan para Android y deja el APK firmado en dist/.
# Uso (desde la raíz del repositorio): nix develop .#android -c android/build.sh
# HILVAN_VERSION: versión a compilar (por defecto la del manifiesto).
# HILVAN_FIRMA: carpeta con las claves de firma (por defecto ../../hilvan-firma, FUERA del repositorio).
set -euo pipefail
cd "$(dirname "$0")"
SRC="$(cd .. && pwd)"
FIRMA="${HILVAN_FIRMA:-$(cd ../.. && pwd)/hilvan-firma}"
: "${ANDROID_HOME:?Correlo dentro de: nix develop .#android}"
BT="$ANDROID_HOME/build-tools/35.0.0"
JAR="$ANDROID_HOME/platforms/android-35/android.jar"
TC="$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/linux-x86_64/bin"
VERSION="${HILVAN_VERSION:-$(sed -n 's/.*android:versionName="\([^"]*\)".*/\1/p' AndroidManifest.xml)}"
VERSION="${VERSION#v}"
# versionCode tiene que crecer en cada versión: 1.2.3 → 10203.
IFS=. read -r V1 V2 V3 <<<"${VERSION%%-*}"
VCODE=$((V1 * 10000 + V2 * 100 + V3))
OUT=build
rm -rf "$OUT/res" "$OUT/classes" "$OUT/gen" "$OUT/apk" && mkdir -p "$OUT"/{res,classes,gen,apk/lib}

echo "1/5 Motor (Go) para arm64 y x86_64"
for abi in "arm64-v8a arm64 aarch64-linux-android29-clang" "x86_64 amd64 x86_64-linux-android29-clang"; do
  set -- $abi
  mkdir -p "$OUT/apk/lib/$1"
  (cd "$SRC" && CGO_ENABLED=1 GOOS=android GOARCH=$2 CC="$TC/$3" \
    go build -tags goolm,sqlite_fts5 -buildmode=c-shared -trimpath -ldflags "-s -w -X main.version=$VERSION" \
      -o "$OLDPWD/$OUT/apk/lib/$1/libhilvan.so" ./cmd/hilvan-android)
  rm -f "$OUT/apk/lib/$1/libhilvan.h"
done

echo "2/5 Recursos e íconos del lanzador"
rm -rf "$OUT/iconos"
node gen-iconos.mjs "$SRC" "$OUT/iconos"
python3 - "$OUT" <<'PY'
import sys
out = sys.argv[1]
m = open("AndroidManifest.xml").read().replace("<!-- ICONOS -->", open(f"{out}/iconos/alias.xml").read())
open(f"{out}/AndroidManifest.xml", "w").write(m)
PY
"$BT/aapt2" compile --dir res -o "$OUT/res/res.zip"
"$BT/aapt2" compile --dir "$OUT/iconos/res" -o "$OUT/res/iconos.zip"
# HILVAN_DEBUG=1: compilación de pruebas (permite inspeccionar el WebView). Nunca para repartir.
DEBUG_FLAG=""; [ "${HILVAN_DEBUG:-}" = 1 ] && DEBUG_FLAG="--debug-mode"
"$BT/aapt2" link -o "$OUT/base.apk" -I "$JAR" --manifest "$OUT/AndroidManifest.xml" $DEBUG_FLAG \
  --java "$OUT/gen" --min-sdk-version 29 --target-sdk-version 35 \
  --version-name "$VERSION" --version-code "$VCODE" "$OUT/res/res.zip" "$OUT/res/iconos.zip"

echo "3/5 Java"
javac -nowarn --release 17 -classpath "$JAR" -d "$OUT/classes" \
  $(find src "$OUT/gen" -name '*.java')
"$BT/d8" --release --min-api 29 --lib "$JAR" --output "$OUT/apk" $(find "$OUT/classes" -name '*.class')

echo "4/5 Empaquetado"
cp "$OUT/base.apk" "$OUT/sin-firma.apk"
(cd "$OUT/apk" && zip -q -r -0 ../sin-firma.apk lib && zip -q ../sin-firma.apk classes.dex)
"$BT/zipalign" -f -P 16 4 "$OUT/sin-firma.apk" "$OUT/alineado.apk"

echo "5/5 Firma"
SUFFIX=""; [ "${HILVAN_DEBUG:-}" = 1 ] && SUFFIX="-pruebas"
# La clave de firma se crea una sola vez, FUERA del repositorio, y hay que conservarla: las
# actualizaciones tienen que firmarse con la misma (si no, Android pide desinstalar).
mkdir -p "$FIRMA" dist
chmod 700 "$FIRMA"
if [ ! -f "$FIRMA/hilvan.keystore" ]; then
  echo "No hay clave de firma en $FIRMA: se crea una nueva (guardala con copia de seguridad)."
  head -c 24 /dev/urandom | base64 | tr -d '/+=' > "$FIRMA/clave.txt"
  chmod 600 "$FIRMA/clave.txt"
  keytool -genkeypair -keystore "$FIRMA/hilvan.keystore" -alias hilvan -keyalg RSA -keysize 4096 \
    -validity 36500 -storepass "$(cat "$FIRMA/clave.txt")" -dname "CN=Hilvan" >/dev/null 2>&1
fi
"$BT/apksigner" sign --ks "$FIRMA/hilvan.keystore" --ks-key-alias hilvan \
  --ks-pass "file:$FIRMA/clave.txt" --out "dist/hilvan-$VERSION$SUFFIX.apk" "$OUT/alineado.apk"
"$BT/apksigner" verify "dist/hilvan-$VERSION$SUFFIX.apk"
ls -lh "dist/hilvan-$VERSION$SUFFIX.apk"

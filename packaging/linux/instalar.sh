#!/bin/sh
# Instala Hilvan para tu usuario (sin permisos de administrador) desde el .tar.gz.
# Necesita GTK 4, WebKitGTK 6 y GStreamer del sistema (ver LEEME). Si preferís no instalar
# nada, usá el .flatpak.
set -e
cd "$(dirname "$0")"
mkdir -p "$HOME/.local/bin" "$HOME/.local/share/applications"
install -m755 hilvan "$HOME/.local/bin/hilvan"
install -m644 io.github.heelxel.Hilvan.desktop "$HOME/.local/share/applications/"
for s in 16 32 48 64 128 256 512; do
  install -Dm644 "icons/hilvan-$s.png" "$HOME/.local/share/icons/hicolor/${s}x${s}/apps/io.github.heelxel.Hilvan.png"
done
echo "Hilvan instalado en ~/.local/bin (buscalo en el menú de aplicaciones)."

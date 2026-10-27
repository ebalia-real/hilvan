{
  lib,
  buildGoModule,
  callPackage,
  src,
  pkg-config,
  wrapGAppsHook4,
  gtk4,
  webkitgtk_6_0,
  glib-networking,
  gst_all_1,
  libnice,
  pipewire,
  xdg-utils,
}:

let
  # Siempre con Go 1.26.10 o más nuevo (arreglos de seguridad de la biblioteca estándar).
  buildGo = buildGoModule.override { go = callPackage ./go.nix { }; };
  version = lib.removeSuffix "\n" (builtins.readFile ../VERSION);
in
buildGo {
  pname = "hilvan";
  inherit version;
  inherit src;

  vendorHash = "sha256-3NyR3f2Jwuho/D+sUwJ55lYA57CNKlQS1MIWGxfGyeU=";

  subPackages = [ "cmd/hilvan" ];
  tags = [ "goolm" "sqlite_fts5" ];
  ldflags = [ "-s" "-w" "-X main.version=${version}" "-X main.packaging=nix" ];

  nativeBuildInputs = [ pkg-config wrapGAppsHook4 ];

  buildInputs = [
    gtk4
    webkitgtk_6_0
    glib-networking
    gst_all_1.gstreamer
    gst_all_1.gst-plugins-base
    gst_all_1.gst-plugins-good
    gst_all_1.gst-plugins-bad
    gst_all_1.gst-plugins-rs
    gst_all_1.gst-libav
    libnice.out
    pipewire
  ];

  preFixup = ''
    gappsWrapperArgs+=(--prefix PATH : ${lib.makeBinPath [ xdg-utils ]})
  '';

  postInstall = ''
    install -Dm644 assets/io.github.heelxel.Hilvan.desktop $out/share/applications/io.github.heelxel.Hilvan.desktop
    install -Dm644 assets/logo.svg $out/share/icons/hicolor/scalable/apps/io.github.heelxel.Hilvan.svg
    for s in 16 32 48 64 128 256 512; do
      install -Dm644 assets/icons/hilvan-$s.png $out/share/icons/hicolor/''${s}x''${s}/apps/io.github.heelxel.Hilvan.png
    done
  '';

  meta = {
    description = "Cliente Matrix minimalista, rápido y seguro";
    homepage = "https://github.com/heelxel/hilvan";
    license = lib.licenses.agpl3Plus;
    mainProgram = "hilvan";
    platforms = lib.platforms.linux;
  };
}

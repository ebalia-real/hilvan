{ lib, buildGoModule, callPackage, src }:

# Puente SCIM 2.0 → Synapse (sin cgo: un binario chico y estático).
(buildGoModule.override { go = callPackage ./go.nix { }; }) {
  pname = "hilvan-scim";
  version = "0.1.0";
  inherit src;
  vendorHash = "sha256-iDwuJ3GEykKQ6iLiN63PaOxF0kUZwjVXSijRjnQ5nJM=";
  subPackages = [ "cmd/hilvan-scim" ];
  env.CGO_ENABLED = 0;
  ldflags = [ "-s" "-w" ];
  meta = {
    description = "Puente SCIM 2.0 para Synapse: alta, baja y grupos automáticos desde el directorio de la organización";
    mainProgram = "hilvan-scim";
    license = lib.licenses.agpl3Plus;
    platforms = lib.platforms.linux ++ lib.platforms.darwin;
  };
}

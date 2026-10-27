# Go 1.26.10: corrige vulnerabilidades de la biblioteca estándar (net/http, crypto/tls,
# crypto/x509…) que tienen las versiones anteriores. Se compila desde el código oficial de go.dev.
{ go_1_26, fetchurl }:
go_1_26.overrideAttrs (old: rec {
  version = "1.26.10";
  src = fetchurl {
    url = "https://go.dev/dl/go${version}.src.tar.gz";
    hash = "sha256-/WPog7ovK/ypPBZGsx6+9j4rHk0rMXyXidl2po2B828=";
  };
})

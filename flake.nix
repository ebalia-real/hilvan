{
  description = "Hilvan — cliente Matrix minimalista, rápido y seguro (Go + GTK4/WebKitGTK + GStreamer)";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAll (pkgs: rec {
        hilvan = pkgs.callPackage ./nix/package.nix { src = self; };
        hilvan-scim = pkgs.callPackage ./nix/scim.nix { src = self; };
        default = hilvan;
      });

      overlays.default = final: prev: {
        hilvan = final.callPackage ./nix/package.nix { src = self; };
        hilvan-scim = final.callPackage ./nix/scim.nix { src = self; };
      };

      # Servidor para organizaciones: Synapse escalado, LDAP/OIDC/SCIM, S3, retención, TURN, LiveKit…
      nixosModules.hilvan-server = { pkgs, ... }: {
        imports = [ ./nix/hilvan-server.nix ];
        nixpkgs.overlays = [ self.overlays.default ];
      };

      apps = forAll (pkgs: {
        default = {
          type = "app";
          program = "${self.packages.${pkgs.stdenv.hostPlatform.system}.hilvan}/bin/hilvan";
        };
      });

      devShells = forAll (pkgs:
        let
          system = pkgs.stdenv.hostPlatform.system;
          go = pkgs.callPackage ./nix/go.nix { };
          desktop = pkgs.mkShell {
            inputsFrom = [ self.packages.${system}.hilvan ];
            packages = [ go pkgs.gopls pkgs.librsvg ];
            shellHook = ''
              export CGO_ENABLED=1
              echo "Hilvan — go build -tags goolm,sqlite_fts5 ./cmd/hilvan"
            '';
          };
        in
        { default = desktop; } // nixpkgs.lib.optionalAttrs (system == "x86_64-linux") (
          let
            # App de Android: SDK, NDK, Java y Go. La licencia del SDK de Android
            # (https://developer.android.com/studio/terms) se acepta solo dentro de estos entornos.
            apkgs = import nixpkgs { inherit system; config = { android_sdk.accept_license = true; allowUnfree = true; }; };
            android = apkgs.androidenv.composeAndroidPackages {
              platformVersions = [ "35" ];
              buildToolsVersions = [ "35.0.0" ];
              includeNDK = true;
              ndkVersions = [ "27.2.12479018" ];
              includeEmulator = false;
              includeSystemImages = false;
              includeSources = false;
            };
            testing = apkgs.androidenv.composeAndroidPackages {
              platformVersions = [ "35" ];
              includeEmulator = true;
              includeSystemImages = true;
              systemImageTypes = [ "default" ];
              abiVersions = [ "x86_64" ];
              includeNDK = false;
            };
            sdk = "${android.androidsdk}/libexec/android-sdk";
          in
          {
            # nix develop .#android -c android/build.sh
            android = apkgs.mkShell {
              packages = [ apkgs.jdk17 go android.androidsdk apkgs.librsvg apkgs.nodejs apkgs.zip ];
              ANDROID_HOME = sdk;
              ANDROID_NDK_HOME = "${sdk}/ndk/27.2.12479018";
            };
            # nix develop .#emulador (pruebas)
            emulador = apkgs.mkShell {
              packages = [ testing.androidsdk ];
              ANDROID_HOME = "${testing.androidsdk}/libexec/android-sdk";
              ANDROID_SDK_ROOT = "${testing.androidsdk}/libexec/android-sdk";
            };
          }));
    };
}

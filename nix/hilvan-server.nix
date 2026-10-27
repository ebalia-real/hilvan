# Hilvan Server: Synapse listo para organizaciones (lo que ofrece Synapse Pro / Element Enterprise),
# en un solo módulo de NixOS:
#   - escala con workers (sync y cliente, federación entrante y saliente, multimedia, notificaciones)
#     y Redis, sobre PostgreSQL con la intercalación que exige Synapse;
#   - identidad corporativa: LDAP / Active Directory, OIDC (SSO) y SCIM (alta y baja automática);
#   - multimedia en S3, retención de mensajes, avisos del servidor y copias de seguridad;
#   - "pasarela de frontera": federación abierta, restringida a una lista o apagada, nginx con TLS,
#     límites contra fuerza bruta y tamaño máximo de archivos;
#   - llamadas: TURN (coturn) y MatrixRTC con LiveKit para llamadas grupales;
#   - la política de la organización para Hilvan publicada en .well-known (marca, funciones, cifrado…).
#
# Uso mínimo:
#   imports = [ hilvan.nixosModules.hilvan-server ];
#   services.hilvan-server = { enable = true; domain = "ejemplo.com"; acmeEmail = "admin@ejemplo.com"; };
{ config, lib, pkgs, ... }:

let
  cfg = config.services.hilvan-server;
  inherit (lib) mkOption mkEnableOption mkIf mkMerge types optional optionals optionalAttrs;

  sock = name: "/run/matrix-synapse/${name}.sock";
  workerNames = prefix: n: map (i: "${prefix}-${toString i}") (lib.range 1 n);
  clientWorkers = workerNames "cliente" cfg.scale.clientWorkers;
  fedReceivers = workerNames "federacion-entrada" (if cfg.federation.enable then cfg.scale.federationReceivers else 0);
  fedSenders = workerNames "federacion-salida" (if cfg.federation.enable then cfg.scale.federationSenders else 0);
  useWorkers = cfg.scale.enable;

  httpWorker = names: {
    worker_listeners = [{
      type = "http";
      path = sock (builtins.head names);
      resources = [{ names = [ "client" "federation" ]; compress = false; }];
    }];
  };

  wellKnownClient = {
    "m.homeserver".base_url = "https://${cfg.matrixHost}";
  } // optionalAttrs (cfg.policy != { }) {
    "io.hilvan.policy" = cfg.policy;
  } // optionalAttrs cfg.calls.matrixRtc.enable {
    "org.matrix.msc4143.rtc_foci" = [{ type = "livekit"; livekit_service_url = "https://${cfg.matrixHost}/livekit/jwt"; }];
  };

  # Rutas que atienden los workers (según la documentación de Synapse); el resto va al proceso principal.
  syncRoutes = [
    "~ ^/_matrix/client/(r0|v3)/sync$"
    "~ ^/_matrix/client/(api/v1|r0|v3)/events$"
    "~ ^/_matrix/client/(api/v1|r0|v3)/initialSync$"
    "~ ^/_matrix/client/(api/v1|r0|v3)/rooms/[^/]+/initialSync$"
    "~ ^/_matrix/client/unstable/org.matrix.simplified_msc3575/sync$"
  ];
  clientRoutes = [
    "~ ^/_matrix/client/(api/v1|r0|v3|unstable)/createRoom$"
    "~ ^/_matrix/client/(api/v1|r0|v3|unstable)/publicRooms$"
    "~ ^/_matrix/client/(api/v1|r0|v3|unstable)/rooms/.*/joined_members$"
    "~ ^/_matrix/client/(api/v1|r0|v3|unstable)/rooms/.*/context/.*$"
    "~ ^/_matrix/client/(api/v1|r0|v3|unstable)/rooms/.*/members$"
    "~ ^/_matrix/client/(api/v1|r0|v3|unstable)/rooms/.*/state$"
    "~ ^/_matrix/client/v1/rooms/.*/hierarchy$"
    "~ ^/_matrix/client/(v1|unstable)/rooms/.*/relations/"
    "~ ^/_matrix/client/v1/rooms/.*/threads$"
    "~ ^/_matrix/client/(api/v1|r0|v3|unstable)/login$"
    "~ ^/_matrix/client/(r0|v3|unstable)/account/3pid$"
    "~ ^/_matrix/client/(r0|v3|unstable)/account/whoami$"
    "~ ^/_matrix/client/(r0|v3|unstable)/devices$"
    "~ ^/_matrix/client/versions$"
    "~ ^/_matrix/client/(api/v1|r0|v3|unstable)/voip/turnServer$"
    "~ ^/_matrix/client/(api/v1|r0|v3|unstable)/rooms/.*/event/"
    "~ ^/_matrix/client/(api/v1|r0|v3|unstable)/joined_rooms$"
    "~ ^/_matrix/client/(api/v1|r0|v3|unstable/.*)/rooms/.*/aliases"
    "~ ^/_matrix/client/(api/v1|r0|v3|unstable)/search$"
    "~ ^/_matrix/client/(r0|v3|unstable)/user/.*/filter(/|$)"
    "~ ^/_matrix/client/(r0|v3|unstable)/keys/query$"
    "~ ^/_matrix/client/(r0|v3|unstable)/keys/changes$"
    "~ ^/_matrix/client/(r0|v3|unstable)/keys/claim$"
    "~ ^/_matrix/client/(r0|v3|unstable)/room_keys/"
    "~ ^/_matrix/client/(r0|v3|unstable)/keys/upload$"
    "~ ^/_matrix/client/(api/v1|r0|v3|unstable)/rooms/.*/redact"
    "~ ^/_matrix/client/(api/v1|r0|v3|unstable)/rooms/.*/send"
    "~ ^/_matrix/client/(api/v1|r0|v3|unstable)/rooms/.*/state/"
    "~ ^/_matrix/client/(api/v1|r0|v3|unstable)/rooms/.*/(join|invite|leave|ban|unban|kick)$"
    "~ ^/_matrix/client/(api/v1|r0|v3|unstable)/join/"
    "~ ^/_matrix/client/(api/v1|r0|v3|unstable)/knock/"
    "~ ^/_matrix/client/(api/v1|r0|v3|unstable)/profile/"
    "~ ^/_matrix/client/(api/v1|r0|v3|unstable)/rooms/.*/messages$"
  ];
  fedRoutes = [ "~ ^/_matrix/federation/" "~ ^/_matrix/key/v2/query" ];
  mediaRoutes = [ "~ ^/_matrix/media/" "~ ^/_matrix/client/v1/media/" "~ ^/_synapse/admin/v1/(purge_media_cache|room/.*/media.*|user/.*/media.*|media/.*|quarantine_media/.*|users/.*/media)$" ];

  # JSON dentro de comillas simples de nginx (un apóstrofo en el nombre de la organización no rompe nada).
  jsonForNginx = v: lib.replaceStrings [ "\\" "'" ] [ "\\\\" "\\'" ] (builtins.toJSON v);

  upstream = name: names: {
    servers = lib.listToAttrs (map (n: { name = "unix:${sock n}"; value = { }; }) names);
    extraConfig = "keepalive 16;";
  };

  proxyTo = up: {
    proxyPass = "http://${up}";
    extraConfig = ''
      proxy_set_header X-Forwarded-For $remote_addr;
      proxy_set_header X-Forwarded-Proto $scheme;
      proxy_set_header Host $host;
      proxy_http_version 1.1;
      proxy_set_header Connection "";
      proxy_read_timeout 600s;
      proxy_request_buffering off;
      client_max_body_size ${cfg.maxUploadSize};
    '';
  };
in
{
  options.services.hilvan-server = {
    enable = mkEnableOption "Hilvan Server (Synapse para organizaciones)";

    domain = mkOption {
      type = types.str;
      example = "ejemplo.com";
      description = "Nombre del servidor Matrix (la parte después de los dos puntos en @usuario:ejemplo.com). No se puede cambiar después.";
    };
    matrixHost = mkOption {
      type = types.str;
      default = "matrix.${cfg.domain}";
      defaultText = "matrix.\${domain}";
      description = "Dominio donde atiende Synapse (y la interfaz de LiveKit / SCIM).";
    };
    acmeEmail = mkOption {
      type = types.nullOr types.str;
      default = null;
      description = "Correo para los certificados de Let's Encrypt. Si es null, configurá vos el TLS de nginx.";
    };
    maxUploadSize = mkOption {
      type = types.str;
      default = "10G";
      description = "Tamaño máximo por archivo (Synapse y nginx juntos, para que no haya un límite escondido).";
    };

    scale = {
      enable = mkOption { type = types.bool; default = true; description = "Repartir la carga en procesos (workers) de Synapse."; };
      clientWorkers = mkOption { type = types.ints.positive; default = 2; description = "Procesos para sync y pedidos de clientes."; };
      federationReceivers = mkOption { type = types.ints.positive; default = 1; };
      federationSenders = mkOption { type = types.ints.positive; default = 1; };
    };

    registration = {
      enable = mkOption { type = types.bool; default = false; description = "Permitir que la gente cree cuentas."; };
      requireToken = mkOption { type = types.bool; default = true; description = "Exigir un código de registro (se crean desde el panel de administración de Hilvan)."; };
    };

    federation = {
      enable = mkOption { type = types.bool; default = true; description = "Hablar con otros servidores Matrix."; };
      allowList = mkOption {
        type = types.nullOr (types.listOf types.str);
        default = null;
        example = [ "socio.com" "matrix.org" ];
        description = "Si se define, solo se federa con estos servidores (pasarela de frontera).";
      };
    };

    retention = {
      enable = mkOption { type = types.bool; default = false; description = "Borrado automático de mensajes según políticas."; };
      defaultDays = mkOption { type = types.nullOr types.ints.positive; default = null; description = "Política por defecto para todas las salas (null = solo las que definan la suya)."; };
      maxDays = mkOption { type = types.nullOr types.ints.positive; default = null; description = "Máximo que puede pedir una sala."; };
    };

    ldap = {
      enable = mkEnableOption "inicio de sesión con LDAP / Active Directory";
      uri = mkOption { type = types.str; default = "ldaps://ldap.ejemplo.com:636"; };
      base = mkOption { type = types.str; default = "ou=usuarios,dc=ejemplo,dc=com"; };
      bindDn = mkOption { type = types.nullOr types.str; default = null; };
      bindPasswordFile = mkOption { type = types.nullOr types.path; default = null; };
      filter = mkOption { type = types.nullOr types.str; default = null; example = "(memberOf=cn=matrix,ou=grupos,dc=ejemplo,dc=com)"; };
      attributes = mkOption {
        type = types.attrsOf types.str;
        default = { uid = "uid"; mail = "mail"; name = "cn"; };
        description = "Para Active Directory: { uid = \"sAMAccountName\"; mail = \"mail\"; name = \"displayName\"; }";
      };
    };

    oidc = {
      enable = mkEnableOption "inicio de sesión único (OIDC)";
      idpName = mkOption { type = types.str; default = "Cuenta de la organización"; };
      issuer = mkOption { type = types.str; default = ""; };
      clientId = mkOption { type = types.str; default = ""; };
      clientSecretFile = mkOption { type = types.nullOr types.path; default = null; };
      scopes = mkOption { type = types.listOf types.str; default = [ "openid" "profile" "email" ]; };
    };

    s3 = {
      enable = mkEnableOption "guardar la multimedia en S3 (o compatible: MinIO, Garage, Ceph…)";
      bucket = mkOption { type = types.str; default = ""; };
      region = mkOption { type = types.nullOr types.str; default = null; };
      endpoint = mkOption { type = types.nullOr types.str; default = null; example = "https://s3.ejemplo.com"; };
      credentialsFile = mkOption {
        type = types.nullOr types.path;
        default = null;
        description = "Archivo con AWS_ACCESS_KEY_ID=… y AWS_SECRET_ACCESS_KEY=… (formato EnvironmentFile de systemd).";
      };
    };

    calls = {
      turn = {
        enable = mkOption { type = types.bool; default = true; description = "Servidor TURN (coturn) para llamadas detrás de NAT."; };
        secretFile = mkOption { type = types.nullOr types.path; default = null; description = "Secreto compartido TURN (si es null se genera uno en el primer arranque)."; };
      };
      matrixRtc = {
        enable = mkOption { type = types.bool; default = false; description = "Llamadas grupales MatrixRTC con LiveKit."; };
        keyFile = mkOption {
          type = types.nullOr types.path;
          default = null;
          description = "Claves de LiveKit (\"clave: secreto\"). Si es null se genera en el primer arranque.";
        };
      };
    };

    policy = mkOption {
      type = types.attrsOf types.anything;
      default = { };
      example = {
        organization = "Ejemplo SA";
        force_encryption = true;
        disable = [ "public_rooms" ];
        brand = { name = "Chat de Ejemplo"; accent = "#0f766e"; };
        retention_default_days = 365;
      };
      description = "Política de la organización para Hilvan (se publica en .well-known/matrix/client como io.hilvan.policy).";
    };

    scim = {
      enable = mkEnableOption "puente SCIM 2.0 (alta, baja y grupos automáticos desde el directorio de la organización)";
      port = mkOption { type = types.port; default = 8090; };
      tokenFile = mkOption { type = types.nullOr types.path; default = null; description = "Token que debe presentar el directorio (Entra ID, Okta, Keycloak…)."; };
      adminTokenFile = mkOption { type = types.nullOr types.path; default = null; description = "Token de acceso de una cuenta administradora de Synapse."; };
      package = mkOption { type = types.package; default = pkgs.hilvan-scim or (throw "Agregá el overlay de Hilvan o definí services.hilvan-server.scim.package"); defaultText = "pkgs.hilvan-scim"; };
    };

    serverNotices = mkOption { type = types.bool; default = true; description = "Habilita los avisos oficiales del servidor (se envían desde el panel de Hilvan)."; };

    backups = {
      enable = mkOption { type = types.bool; default = true; description = "Copia diaria de la base de datos."; };
      location = mkOption { type = types.str; default = "/var/backup/hilvan"; };
    };
  };

  config = mkIf cfg.enable (mkMerge [
    {
      assertions = [
        { assertion = !cfg.ldap.enable || cfg.ldap.bindPasswordFile != null || cfg.ldap.bindDn == null; message = "hilvan-server: con ldap.bindDn hace falta ldap.bindPasswordFile."; }
        { assertion = !cfg.oidc.enable || (cfg.oidc.issuer != "" && cfg.oidc.clientId != "" && cfg.oidc.clientSecretFile != null); message = "hilvan-server: OIDC necesita issuer, clientId y clientSecretFile."; }
        { assertion = !cfg.s3.enable || cfg.s3.bucket != ""; message = "hilvan-server: S3 necesita un bucket."; }
        { assertion = !cfg.scim.enable || (cfg.scim.tokenFile != null && cfg.scim.adminTokenFile != null); message = "hilvan-server: SCIM necesita tokenFile y adminTokenFile."; }
      ];

      # --- base de datos (Synapse exige intercalación C) ---
      services.postgresql.enable = true;

      services.matrix-synapse = {
        enable = true;
        # Synapse atiende por sockets UNIX (detrás de nginx); las cuentas se crean desde el panel de
        # administración de Hilvan o por SCIM, no con register_new_matrix_user.
        enableRegistrationScript = false;
        configureRedisLocally = useWorkers;
        extras = [ "oidc" "postgres" "redis" "url-preview" "systemd" ];
        plugins = optional cfg.ldap.enable pkgs.matrix-synapse-plugins.matrix-synapse-ldap3
          ++ optional cfg.s3.enable pkgs.matrix-synapse-plugins.matrix-synapse-s3-storage-provider;
        settings = {
          server_name = cfg.domain;
          public_baseurl = "https://${cfg.matrixHost}/";
          database = { name = "psycopg2"; args = { user = "matrix-synapse"; database = "matrix-synapse"; host = "/run/postgresql"; }; };
          listeners = [{
            path = sock "principal";
            type = "http";
            resources = [{ names = [ "client" ] ++ optional cfg.federation.enable "federation"; compress = false; }];
          }] ++ optional useWorkers {
            path = "/run/matrix-synapse/main_replication.sock";
            type = "http";
            resources = [{ names = [ "replication" ]; compress = false; }];
          };
          max_upload_size = cfg.maxUploadSize;
          enable_registration = cfg.registration.enable;
          registration_requires_token = cfg.registration.enable && cfg.registration.requireToken;
          enable_registration_without_verification = cfg.registration.enable && cfg.registration.requireToken;
          url_preview_enabled = true;
          url_preview_ip_range_blacklist = [ "127.0.0.0/8" "10.0.0.0/8" "172.16.0.0/12" "192.168.0.0/16" "100.64.0.0/10" "169.254.0.0/16" "::1/128" "fe80::/10" "fc00::/7" ];
          # Sin informes de uso a terceros.
          report_stats = false;
          # Llamadas grupales y Sliding Sync que usa Hilvan.
          experimental_features = { msc3266_enabled = true; msc4222_enabled = true; } // optionalAttrs cfg.calls.matrixRtc.enable { msc4140_enabled = true; };
          max_event_delay_duration = "24h";
          user_directory = { enabled = true; search_all_users = true; prefer_local_users = true; };
        }
        // optionalAttrs (!cfg.federation.enable) { federation_domain_whitelist = [ ]; }
        // optionalAttrs (cfg.federation.enable && cfg.federation.allowList != null) { federation_domain_whitelist = cfg.federation.allowList; }
        // optionalAttrs cfg.retention.enable {
          retention = {
            enabled = true;
            purge_jobs = [{ interval = "12h"; }];
          } // optionalAttrs (cfg.retention.defaultDays != null) {
            default_policy = { max_lifetime = "${toString cfg.retention.defaultDays}d"; };
          } // optionalAttrs (cfg.retention.maxDays != null) {
            allowed_lifetime_max = "${toString cfg.retention.maxDays}d";
          };
        }
        // optionalAttrs cfg.serverNotices {
          server_notices = { system_mxid_localpart = "avisos"; system_mxid_display_name = "Avisos del servidor"; room_name = "Avisos del servidor"; };
        }
        // optionalAttrs cfg.ldap.enable {
          modules = [{
            module = "ldap_auth_provider.LdapAuthProviderModule";
            config = {
              enabled = true;
              uri = cfg.ldap.uri;
              start_tls = lib.hasPrefix "ldap://" cfg.ldap.uri;
              base = cfg.ldap.base;
              attributes = cfg.ldap.attributes;
            } // optionalAttrs (cfg.ldap.bindDn != null) { bind_dn = cfg.ldap.bindDn; bind_password_file = cfg.ldap.bindPasswordFile; }
              // optionalAttrs (cfg.ldap.filter != null) { filter = cfg.ldap.filter; };
          }];
        }
        // optionalAttrs cfg.oidc.enable {
          oidc_providers = [{
            idp_id = "organizacion";
            idp_name = cfg.oidc.idpName;
            issuer = cfg.oidc.issuer;
            client_id = cfg.oidc.clientId;
            client_secret_path = cfg.oidc.clientSecretFile;
            scopes = cfg.oidc.scopes;
            user_mapping_provider.config = {
              localpart_template = "{{ user.preferred_username }}";
              display_name_template = "{{ user.name }}";
              email_template = "{{ user.email }}";
            };
            allow_existing_users = true;
          }];
        }
        // optionalAttrs cfg.s3.enable {
          media_storage_providers = [{
            module = "s3_storage_provider.S3StorageProviderBackend";
            store_local = true;
            store_remote = true;
            store_synchronous = true;
            config = { bucket = cfg.s3.bucket; }
              // optionalAttrs (cfg.s3.region != null) { region_name = cfg.s3.region; }
              // optionalAttrs (cfg.s3.endpoint != null) { endpoint_url = cfg.s3.endpoint; };
          }];
        }
        // optionalAttrs useWorkers {
          federation_sender_instances = fedSenders;
          pusher_instances = [ "notificaciones" ];
          media_instance_running_background_jobs = "multimedia";
          enable_media_repo = false;
          run_background_tasks_on = "master"; # el proceso principal
          notify_appservices_from_worker = "notificaciones";
          update_user_directory_from_worker = "notificaciones";
        }
        // optionalAttrs (cfg.calls.turn.enable) {
          turn_uris = [ "turn:${cfg.matrixHost}:3478?transport=udp" "turn:${cfg.matrixHost}:3478?transport=tcp" "turns:${cfg.matrixHost}:5349?transport=tcp" ];
          turn_user_lifetime = "1d";
          turn_allow_guests = false;
        };

        workers = mkIf useWorkers (lib.listToAttrs (
          map (n: { name = n; value = httpWorker [ n ]; }) (clientWorkers ++ fedReceivers)
          ++ map (n: { name = n; value = { }; }) fedSenders
          ++ [
            { name = "notificaciones"; value = { }; }
            {
              name = "multimedia";
              value = {
                worker_app = "synapse.app.media_repository";
                worker_listeners = [{ type = "http"; path = sock "multimedia"; resources = [{ names = [ "media" ]; compress = false; }]; }];
              };
            }
          ]
        ));
      };

      # Secretos de TURN generados una sola vez si no se dan.
      systemd.services.hilvan-server-secrets = {
        description = "Hilvan Server: secretos iniciales";
        wantedBy = [ "multi-user.target" ];
        before = [ "matrix-synapse.service" "coturn.service" "livekit.service" "lk-jwt-service.service" ];
        serviceConfig = { Type = "oneshot"; RemainAfterExit = true; };
        script = ''
          set -eu
          install -d -m 0755 /var/lib/hilvan-server
          if [ ! -s /var/lib/hilvan-server/turn-secret ]; then ${pkgs.openssl}/bin/openssl rand -hex 32 > /var/lib/hilvan-server/turn-secret; fi
          if [ ! -s /var/lib/hilvan-server/livekit.keys ]; then
            echo "hilvan: $(${pkgs.openssl}/bin/openssl rand -hex 32)" > /var/lib/hilvan-server/livekit.keys
          fi
          printf 'turn_shared_secret: "%s"\n' "$(cat ${if cfg.calls.turn.secretFile != null then toString cfg.calls.turn.secretFile else "/var/lib/hilvan-server/turn-secret"})" > /var/lib/hilvan-server/synapse-turn.yaml
          chown -R matrix-synapse:matrix-synapse /var/lib/hilvan-server/synapse-turn.yaml || true
          chmod 0640 /var/lib/hilvan-server/*
          chgrp turnserver /var/lib/hilvan-server/turn-secret 2>/dev/null || true
        '';
      };
      services.matrix-synapse.extraConfigFiles = optional cfg.calls.turn.enable "/var/lib/hilvan-server/synapse-turn.yaml";

      # --- nginx: TLS, .well-known, workers, límites ---
      services.nginx = {
        enable = true;
        recommendedProxySettings = false;
        recommendedTlsSettings = true;
        recommendedOptimisation = true;
        recommendedGzipSettings = true;
        appendHttpConfig = ''
          # Pasarela de frontera: frena la fuerza bruta contra inicio de sesión y registro.
          limit_req_zone $binary_remote_addr zone=hilvan_login:10m rate=5r/m;
          limit_req_zone $binary_remote_addr zone=hilvan_api:10m rate=50r/s;
        '';
        upstreams = {
          "synapse_principal" = upstream "principal" [ "principal" ];
        } // optionalAttrs useWorkers ({
          "synapse_clientes" = upstream "clientes" clientWorkers;
          "synapse_multimedia" = upstream "multimedia" [ "multimedia" ];
        } // optionalAttrs cfg.federation.enable { "synapse_federacion" = upstream "federacion" fedReceivers; });
        virtualHosts.${cfg.domain} = {
          enableACME = cfg.acmeEmail != null;
          forceSSL = cfg.acmeEmail != null;
          locations."= /.well-known/matrix/server".extraConfig = ''
            default_type application/json;
            add_header Access-Control-Allow-Origin *;
            return 200 '${jsonForNginx { "m.server" = "${cfg.matrixHost}:443"; }}';
          '';
          locations."= /.well-known/matrix/client".extraConfig = ''
            default_type application/json;
            add_header Access-Control-Allow-Origin *;
            return 200 '${jsonForNginx wellKnownClient}';
          '';
        };
        virtualHosts.${cfg.matrixHost} = {
          enableACME = cfg.acmeEmail != null;
          forceSSL = cfg.acmeEmail != null;
          extraConfig = "client_max_body_size ${cfg.maxUploadSize};";
          locations = lib.mkMerge [
            {
              "/_matrix" = proxyTo "synapse_principal";
              "/_synapse/client" = proxyTo "synapse_principal";
              "/_synapse/admin" = proxyTo "synapse_principal";
              # Va antes que las rutas de los workers (las regex de nginx se evalúan en orden).
              "~ ^/_matrix/client/(r0|v3)/(login|register)" = (proxyTo "synapse_principal") // {
                priority = 100;
                extraConfig = (proxyTo "synapse_principal").extraConfig + "limit_req zone=hilvan_login burst=10 nodelay;";
              };
              "= /.well-known/matrix/client".extraConfig = ''
                default_type application/json;
                add_header Access-Control-Allow-Origin *;
                return 200 '${jsonForNginx wellKnownClient}';
              '';
            }
            (mkIf useWorkers (lib.listToAttrs (map (r: { name = r; value = proxyTo "synapse_clientes"; }) (syncRoutes ++ clientRoutes))
              // lib.listToAttrs (map (r: { name = r; value = proxyTo "synapse_multimedia"; }) mediaRoutes)
              // optionalAttrs cfg.federation.enable (lib.listToAttrs (map (r: { name = r; value = proxyTo "synapse_federacion"; }) fedRoutes))))
            (mkIf cfg.calls.matrixRtc.enable {
              "/livekit/jwt/".proxyPass = "http://127.0.0.1:${toString config.services.lk-jwt-service.port}/";
              "/livekit/sfu/" = {
                proxyPass = "http://127.0.0.1:${toString (config.services.livekit.settings.port or 7880)}/";
                proxyWebsockets = true;
              };
            })
            (mkIf cfg.scim.enable {
              "/scim/v2/".proxyPass = "http://127.0.0.1:${toString cfg.scim.port}/scim/v2/";
            })
          ];
        };
      };
      security.acme = mkIf (cfg.acmeEmail != null) { acceptTerms = true; defaults.email = cfg.acmeEmail; };
      users.users.nginx.extraGroups = [ "matrix-synapse" ];
      networking.firewall.allowedTCPPorts = [ 80 443 ];

      # La base se crea aunque PostgreSQL ya existiera (initialScript solo corre la primera vez).
      systemd.services.hilvan-server-db = {
        description = "Hilvan Server: base de datos de Synapse";
        wantedBy = [ "multi-user.target" ];
        after = [ "postgresql.service" ];
        requires = [ "postgresql.service" ];
        before = [ "matrix-synapse.service" ];
        serviceConfig = { Type = "oneshot"; RemainAfterExit = true; User = "postgres"; };
        script = ''
          psql=${config.services.postgresql.package}/bin/psql
          $psql -tAc "SELECT 1 FROM pg_roles WHERE rolname='matrix-synapse'" | grep -q 1 || $psql -c 'CREATE ROLE "matrix-synapse" LOGIN'
          $psql -tAc "SELECT 1 FROM pg_database WHERE datname='matrix-synapse'" | grep -q 1 || \
            $psql -c 'CREATE DATABASE "matrix-synapse" WITH OWNER "matrix-synapse" TEMPLATE template0 LC_COLLATE = "C" LC_CTYPE = "C"'
        '';
      };
    }

    # --- S3: credenciales para todos los procesos de Synapse ---
    (mkIf (cfg.s3.enable && cfg.s3.credentialsFile != null) {
      systemd.services = lib.genAttrs ([ "matrix-synapse" ] ++ map (n: "matrix-synapse-worker-${n}") (lib.attrNames config.services.matrix-synapse.workers))
        (_: { serviceConfig.EnvironmentFile = [ cfg.s3.credentialsFile ]; });
    })

    # --- TURN ---
    (mkIf cfg.calls.turn.enable {
      services.coturn = {
        enable = true;
        realm = cfg.matrixHost;
        use-auth-secret = true;
        static-auth-secret-file = if cfg.calls.turn.secretFile != null then toString cfg.calls.turn.secretFile else "/var/lib/hilvan-server/turn-secret";
        no-tcp-relay = true;
        extraConfig = ''
          no-multicast-peers
          denied-peer-ip=10.0.0.0-10.255.255.255
          denied-peer-ip=172.16.0.0-172.31.255.255
          denied-peer-ip=192.168.0.0-192.168.255.255
          denied-peer-ip=127.0.0.0-127.255.255.255
          denied-peer-ip=100.64.0.0-100.127.255.255
        '';
      };
      networking.firewall.allowedUDPPorts = [ 3478 5349 ];
      networking.firewall.allowedTCPPorts = [ 3478 5349 ];
      networking.firewall.allowedUDPPortRanges = [{ from = config.services.coturn.min-port; to = config.services.coturn.max-port; }];
    })

    # --- MatrixRTC: LiveKit + servicio de credenciales ---
    (mkIf cfg.calls.matrixRtc.enable {
      services.livekit = {
        enable = true;
        openFirewall = true;
        keyFile = if cfg.calls.matrixRtc.keyFile != null then cfg.calls.matrixRtc.keyFile else "/var/lib/hilvan-server/livekit.keys";
        settings.room.auto_create = false;
      };
      services.lk-jwt-service = {
        enable = true;
        livekitUrl = "wss://${cfg.matrixHost}/livekit/sfu";
        keyFile = if cfg.calls.matrixRtc.keyFile != null then cfg.calls.matrixRtc.keyFile else "/var/lib/hilvan-server/livekit.keys";
      };
    })

    # --- SCIM ---
    (mkIf cfg.scim.enable {
      systemd.services.hilvan-scim = {
        description = "Hilvan: puente SCIM 2.0 hacia Synapse";
        wantedBy = [ "multi-user.target" ];
        after = [ "matrix-synapse.target" "network.target" ];
        serviceConfig = {
          ExecStart = lib.escapeShellArgs [
            "${cfg.scim.package}/bin/hilvan-scim"
            "--listen" "127.0.0.1:${toString cfg.scim.port}"
            "--homeserver" "http://localhost"
            "--server-name" cfg.domain
            "--token-file" "%d/scim-token"
            "--admin-token-file" "%d/admin-token"
            "--state" "/var/lib/hilvan-scim/state.json"
          ];
          LoadCredential = [ "scim-token:${toString cfg.scim.tokenFile}" "admin-token:${toString cfg.scim.adminTokenFile}" ];
          DynamicUser = true;
          StateDirectory = "hilvan-scim";
          NoNewPrivileges = true;
          ProtectSystem = "strict";
          ProtectHome = true;
          PrivateTmp = true;
          RestrictAddressFamilies = [ "AF_INET" "AF_INET6" "AF_UNIX" ];
        };
      };
    })

    # --- copias de seguridad ---
    (mkIf cfg.backups.enable {
      services.postgresqlBackup = {
        enable = true;
        databases = [ "matrix-synapse" ];
        location = cfg.backups.location;
        compression = "zstd";
      };
    })
  ]);
}

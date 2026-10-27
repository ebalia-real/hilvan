# Hilvan para organizaciones

Todo lo que ofrecen Synapse Pro / Element Enterprise que se puede hacer con software, en Hilvan:
la app (lado del cliente) y **Hilvan Server**, un módulo de NixOS que deja Synapse listo para una
organización.

## 1. Hilvan Server (NixOS)

En tu flake de NixOS (`/etc/nixos/flake.nix`):

```nix
inputs.hilvan.url = "github:ebalia-real/hilvan";

# dentro de nixosSystem { modules = [ ... ]; }
hilvan.nixosModules.hilvan-server
{
  services.hilvan-server = {
    enable = true;
    domain = "ejemplo.com";              # @usuario:ejemplo.com (no se puede cambiar después)
    acmeEmail = "admin@ejemplo.com";     # certificados TLS automáticos
  };
}
```

Con eso ya tenés Synapse sobre PostgreSQL, repartido en procesos (workers) con Redis, detrás de nginx
con TLS, TURN para llamadas, copias diarias de la base, avisos del servidor y un tope de archivos de
10 GB que coincide en Synapse y en nginx.

| Necesidad | Opción |
|---|---|
| Más capacidad | `scale.clientWorkers`, `scale.federationReceivers`, `scale.federationSenders` |
| Federación solo con socios | `federation.allowList = [ "socio.com" ];` |
| Sin federación | `federation.enable = false;` |
| Retención de mensajes | `retention = { enable = true; defaultDays = 365; maxDays = 1825; };` |
| LDAP / Active Directory | `ldap = { enable = true; uri = "ldaps://…"; base = "…"; bindDn = "…"; bindPasswordFile = …; };` |
| Inicio de sesión único | `oidc = { enable = true; issuer = "…"; clientId = "…"; clientSecretFile = …; };` |
| Multimedia en S3 | `s3 = { enable = true; bucket = "…"; endpoint = "…"; credentialsFile = …; };` |
| Llamadas grupales (MatrixRTC) | `calls.matrixRtc.enable = true;` (LiveKit + servicio de credenciales) |
| Alta/baja automática | `scim = { enable = true; tokenFile = …; adminTokenFile = …; };` |
| Registro abierto con código | `registration = { enable = true; requireToken = true; };` |
| Política para la app | `policy = { … };` (ver abajo) |

Los secretos (`*File`) son rutas a archivos que no van al store de Nix (por ejemplo con sops-nix o
agenix). Para Active Directory usá `ldap.attributes = { uid = "sAMAccountName"; mail = "mail"; name = "displayName"; };`.

### Pasarela de frontera
nginx frena la fuerza bruta contra el inicio de sesión y el registro (5 intentos por minuto por IP),
la federación puede limitarse a una lista, y cada sala puede además tener su propia lista de
servidores permitidos y bloqueados (Ajustes de la sala → Servidores permitidos y bloqueados).

## 2. Política de la organización

Se publica desde el servidor (`services.hilvan-server.policy`, que va a
`.well-known/matrix/client` como `io.hilvan.policy`) o se instala en cada equipo
(`/etc/hilvan/policy.json`, `%ProgramData%\Hilvan\policy.json` en Windows,
`/Library/Application Support/Hilvan/policy.json` en macOS; útil con MDM). Si hay las dos, la del
equipo manda.

```json
{
  "organization": "Ejemplo SA",
  "default_homeserver": "ejemplo.com",
  "allowed_homeservers": ["ejemplo.com"],
  "disable_registration": true,
  "force_encryption": true,
  "disable": ["public_rooms", "url_previews", "jitsi"],
  "brand": { "name": "Chat de Ejemplo", "accent": "#0f766e", "logo_url": "mxc://ejemplo.com/LogoId" },
  "locked_settings": { "proxy": "", "ui.joinMuted": true },
  "cert_pins": { "matrix.ejemplo.com": ["<SHA-256 en base64 de la clave pública>"] },
  "content_scanner": "https://escaner.ejemplo.com",
  "report_url": "https://informes.ejemplo.com/api/submit",
  "retention_default_days": 365,
  "max_accounts": 1,
  "support_url": "https://intranet.ejemplo.com/soporte"
}
```

- **disable** acepta: `calls`, `screenshare`, `jitsi`, `url_previews`, `public_rooms`, `bridges`,
  `labs`, `export`, `file_upload`, `multi_account`.
- **locked_settings**: cualquier ajuste de Hilvan; los de la interfaz llevan el prefijo `ui.`.
  La persona ve "administrado por tu organización" y no puede cambiarlos.
- **cert_pins**: si el servidor presenta otra clave (aunque el certificado sea "válido"), Hilvan no se
  conecta. Para calcular el valor:
  `openssl s_client -connect matrix.ejemplo.com:443 </dev/null | openssl x509 -pubkey -noout | openssl pkey -pubin -outform der | openssl dgst -sha256 -binary | base64`
- **content_scanner**: un [matrix-content-scanner](https://github.com/element-hq/matrix-content-scanner-python);
  Hilvan consulta cada archivo antes de mostrarlo o guardarlo y bloquea los infectados.
- **report_url**: servidor compatible con rageshake. Sin esto, los informes se guardan en el equipo.
  Hilvan no tiene telemetría.

## 3. SCIM (Entra ID, Okta, Keycloak, authentik…)

Con `services.hilvan-server.scim.enable` el directorio apunta a `https://matrix.ejemplo.com/scim/v2`
con el token de `scim.tokenFile`:

- **Usuarios**: alta (con nombre, correo e identificador externo), cambios, y baja (`active=false` o
  borrar) que desactiva la cuenta sin borrar sus mensajes.
- **Grupos**: cada grupo es un **espacio** de Matrix; entrar o salir del grupo agrega o quita del
  espacio. Las salas que pongas dentro del espacio con acceso "miembros del espacio" quedan
  disponibles solas para ese grupo.

`scim.adminTokenFile` es el token de acceso de una cuenta administradora de Synapse.

## 4. En la app

- **Administración** (Ajustes → Administración, cuentas administradoras): usuarios (crear,
  contraseñas, silenciar en secreto, borrar todos sus mensajes, cuarentena de archivos, cerrar
  sesiones, desactivar con borrado de datos), salas (bloquear, tomar el control, eliminar),
  **incorporación masiva** (agregar personas a salas y espacios, crear cuentas desde una lista),
  códigos de registro, **denuncias** con acciones, **avisos del servidor**, **avisos de seguridad**
  (compara tu versión de Synapse con la publicada y con los avisos públicos) y estado de la federación.
- **Vista en cuadrícula**: varias salas a la vez (botón de cuadrícula abajo a la izquierda).
- **Exportación para auditoría**: HTML, JSON o texto, con ediciones, borrados, hilos y estado de
  cifrado, más un `.sha256` para comprobar que no se alteró.
- **Retención por sala**, **salas sin federación** y **moderación** (borrar mensajes recientes de alguien).
- **SBOM**: `hilvan --sbom` o Ajustes → Acerca de → Lista de componentes (CycloneDX).

## 5. Lo que no es software

Soporte con SLA, versiones LTS, capacitación, certificaciones (ISO 27001, Cyber Essentials) y
pasarelas de dominio cruzado por hardware son servicios o equipos, no funciones de un programa.

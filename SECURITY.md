# Seguridad

## Informar una vulnerabilidad

No abras un issue público. Usá **Security → Report a vulnerability** en este repositorio (aviso
privado de GitHub). Respondemos lo antes posible y publicamos el arreglo junto con un aviso.

## Cómo protege Hilvan tus datos

- Cifrado de extremo a extremo de Matrix (Olm/Megolm en Go), verificación de dispositivos y
  respaldo de claves cifrado.
- La interfaz corre dentro de la app y habla con el motor por `127.0.0.1` con un token de un solo
  uso que pasa a una cookie `HttpOnly`/`SameSite=Strict`; se controlan el host (contra *DNS
  rebinding*) y el origen de las conexiones.
- Sin telemetría.

## Actualizaciones firmadas

Cada versión publica `SHA256SUMS` (la huella de cada archivo) y `SHA256SUMS.sig`, una firma
Ed25519 hecha con una clave que **no está en GitHub**: la tiene solo quien publica. El
actualizador de Hilvan verifica esa firma con la clave pública incluida en la app y la huella del
archivo antes de instalar; si algo no coincide, no instala nada. Así, aunque alguien tuviera acceso
a la cuenta o al repositorio, no podría distribuir una actualización que Hilvan acepte.

Para verificar a mano una descarga:

```bash
sha256sum -c SHA256SUMS --ignore-missing
go run ./cmd/hilvan-firmar verificar AFLpj7CpH+ZhXM99dNbP2DS7/GAXq3cqIF9VbHJl8LA= SHA256SUMS
```

El APK de Android además está firmado con la clave de la app: Android solo acepta actualizarlo con
la misma firma.

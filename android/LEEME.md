# Hilvan para Android

La misma app que en la computadora: el motor de Hilvan (Go) corre dentro del teléfono y la
interfaz se muestra en una ventana nativa liviana. Mismas funciones, mismo cifrado.

- **APK listo:** `dist/hilvan-<versión>.apk` (Android 10 o más nuevo; celulares arm64 y emuladores x86_64).
- **Instalar:** pasalo al teléfono y abrilo (Android pide permitir "instalar apps de esta fuente"),
  o con el cable: `adb install -r dist/hilvan-0.1.0.apk`.

## Qué hace en Android
- Queda conectado con la app cerrada (aviso fijo "Hilvan está conectado") y avisa de mensajes,
  menciones, invitaciones y llamadas con notificaciones de Android. Vuelve solo al prender el teléfono.
- Tocar una notificación abre esa conversación. Abre enlaces `matrix:` y `https://matrix.to/…`.
- Mantener apretado un mensaje: el mismo menú que el clic derecho, con **Guardar como…** (selector
  de Android) y **Guardar en Descargas** (carpeta `Descargas/Hilvan`).
- Llamadas de voz y video con el WebRTC del sistema (pide micrófono y cámara la primera vez).
- Ícono de la app: los 15 estilos cambian el ícono del teléfono (Ajustes → Apariencia); "Tu color"
  y "Tu imagen" se usan dentro de la app (Android no permite íconos propios en el lanzador).
- Ajustes → Red y sistema: seguir conectado con la app cerrada, conectarse al prender el teléfono,
  quitar la restricción de batería y abrir los avisos del sistema.
- Zoom con los dedos en las fotos, deslizar para pasar o cerrar.
- No hay compartir pantalla: el WebView de Android no puede capturarla.
- El botón Atrás cierra lo abierto (visor, menú, conversación) y si no hay nada deja la app en segundo plano.

## Compilar
Todo sale de Nix (SDK y NDK de Android, Java 17, Go); la licencia del SDK de Android se acepta en
`flake.nix` (entornos `android` y `emulador`) solo para este proyecto.

    nix develop .#android -c android/build.sh      (desde la raíz del repositorio)

`HILVAN_DEBUG=1` arma `dist/hilvan-…-pruebas.apk`, que permite inspeccionar el WebView (solo para
probar; no repartirla). El motor para Android está en `cmd/hilvan-android`.

## La clave de firma (importante)
La clave está en `../../hilvan-firma/` (fuera del repositorio; otra carpeta con `HILVAN_FIRMA`):
`hilvan.keystore` y su contraseña `clave.txt`.
**Guardalas con copia de seguridad**: las actualizaciones se tienen que firmar con la misma clave;
si se pierde, Android obliga a desinstalar (y se pierden los datos locales) para instalar la nueva.

## Probar en un emulador
    nix develop .#emulador      (desde la raíz del repositorio)
    avdmanager create avd -n hilvan -k "system-images;android-35;default;x86_64" -d pixel_7
    emulator -avd hilvan
Desde el emulador, el servidor de tu computadora es `10.0.2.2`.

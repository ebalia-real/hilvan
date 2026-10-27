// Package assets incluye en el programa los íconos que necesita sin la interfaz web
// (por ejemplo el de la bandeja del sistema).
package assets

import _ "embed"

//go:embed icons/hilvan-256.png
var Icon256 []byte

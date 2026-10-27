// Package web contiene la interfaz (HTML/CSS/JS sin paso de compilación).
package web

import "embed"

//go:embed index.html css js i18n img vendor
var Assets embed.FS

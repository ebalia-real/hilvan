//go:build darwin

package main

import (
	"os/exec"
	"strings"

	webview "github.com/webview/webview_go"
)

func bringToFront(w webview.WebView) {
	_ = exec.Command("osascript", "-e", `tell application "Hilvan" to activate`).Start()
}

func openBrowser(u string) {
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "mailto:") {
		return
	}
	_ = exec.Command("open", u).Start()
}

func asQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// saveDialog: el diálogo "Guardar como" de macOS.
func saveDialog(title, name, folder string) (string, error) {
	script := "POSIX path of (choose file name with prompt " + asQuote(title) + " default name " + asQuote(name) +
		" default location (POSIX file " + asQuote(folder) + "))"
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && strings.Contains(string(ee.Stderr), "-128") {
			return "", nil // cancelado
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

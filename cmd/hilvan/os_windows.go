//go:build windows

package main

import (
	"os/exec"
	"strings"
	"syscall"
	"unsafe"

	webview "github.com/webview/webview_go"
)

var (
	user32            = syscall.NewLazyDLL("user32.dll")
	procShowWindow    = user32.NewProc("ShowWindow")
	procSetForeground = user32.NewProc("SetForegroundWindow")
)

func bringToFront(w webview.WebView) {
	h := uintptr(unsafe.Pointer(w.Window()))
	procShowWindow.Call(h, 9) // SW_RESTORE
	procSetForeground.Call(h)
}

func hidden(c *exec.Cmd) *exec.Cmd {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // sin consola
	return c
}

func openBrowser(u string) {
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "mailto:") {
		return
	}
	_ = hidden(exec.Command("rundll32", "url.dll,FileProtocolHandler", u)).Start()
}

func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// saveDialog: el diálogo "Guardar como" de Windows.
func saveDialog(title, name, folder string) (string, error) {
	script := "Add-Type -AssemblyName System.Windows.Forms;" +
		"$d=New-Object System.Windows.Forms.SaveFileDialog;" +
		"$d.Title=" + psQuote(title) + ";$d.FileName=" + psQuote(name) + ";$d.InitialDirectory=" + psQuote(folder) + ";" +
		"if($d.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK){[Console]::Out.Write($d.FileName)}"
	out, err := hidden(exec.Command("powershell", "-NoProfile", "-STA", "-NonInteractive", "-Command", script)).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

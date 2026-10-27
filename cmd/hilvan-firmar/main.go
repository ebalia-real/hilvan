// hilvan-firmar firma las versiones publicadas para que el actualizador confíe solo en ellas.
//
//	hilvan-firmar clave <archivo>        crea la clave privada (si no existe) e imprime la pública
//	hilvan-firmar firmar <clave> <arch>  escribe <arch>.sig (firma Ed25519 en base64)
//	hilvan-firmar verificar <pública> <arch>
//
// La clave privada nunca se sube a ningún lado: sin ella nadie puede publicar una actualización
// que Hilvan acepte, aunque tenga acceso al repositorio.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func readKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("clave privada inválida")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func run(args []string) error {
	if len(args) < 2 {
		return errors.New("uso: hilvan-firmar clave|firmar|verificar …")
	}
	switch args[0] {
	case "clave":
		if _, err := os.Stat(args[1]); err != nil {
			seed := make([]byte, ed25519.SeedSize)
			if _, err := rand.Read(seed); err != nil {
				return err
			}
			if err := os.WriteFile(args[1], []byte(base64.StdEncoding.EncodeToString(seed)+"\n"), 0o600); err != nil {
				return err
			}
		}
		k, err := readKey(args[1])
		if err != nil {
			return err
		}
		fmt.Println(base64.StdEncoding.EncodeToString(k.Public().(ed25519.PublicKey)))
	case "firmar":
		if len(args) < 3 {
			return errors.New("uso: hilvan-firmar firmar <clave> <archivo>")
		}
		k, err := readKey(args[1])
		if err != nil {
			return err
		}
		data, err := os.ReadFile(args[2])
		if err != nil {
			return err
		}
		sig := base64.StdEncoding.EncodeToString(ed25519.Sign(k, data))
		return os.WriteFile(args[2]+".sig", []byte(sig+"\n"), 0o644)
	case "verificar":
		if len(args) < 3 {
			return errors.New("uso: hilvan-firmar verificar <clave pública> <archivo>")
		}
		pub, err := base64.StdEncoding.DecodeString(args[1])
		if err != nil || len(pub) != ed25519.PublicKeySize {
			return errors.New("clave pública inválida")
		}
		data, err := os.ReadFile(args[2])
		if err != nil {
			return err
		}
		sb, err := os.ReadFile(args[2] + ".sig")
		if err != nil {
			return err
		}
		sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sb)))
		if err != nil || !ed25519.Verify(pub, data, sig) {
			return errors.New("la firma NO es válida")
		}
		fmt.Println("firma válida")
	default:
		return errors.New("comando desconocido")
	}
	return nil
}

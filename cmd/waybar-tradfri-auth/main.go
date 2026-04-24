// waybar-tradfri-auth performs the one-time pairing with a DIRIGERA
// hub and writes the resulting access token to disk. Run this once
// after setting up the hub; the daemon binary reads the token from the
// same path.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/polarn/waybar-modules/pkg/dirigera"
)

const defaultTokenRel = ".config/waybar-tradfri/token"

func main() {
	host := flag.String("host", "", "DIRIGERA hostname or IP (required)")
	name := flag.String("name", "waybar-tradfri", "Client name shown in the IKEA app")
	tokenPath := flag.String("output", "", "Token file path (default $HOME/"+defaultTokenRel+")")
	flag.Parse()

	if *host == "" {
		log.Fatal("--host is required")
	}

	out := *tokenPath
	if out == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("resolve home dir: %v", err)
		}
		out = filepath.Join(home, defaultTokenRel)
	}

	ac := dirigera.NewAuthClient(*host)

	fmt.Printf("Requesting authorization code from %s…\n", *host)
	code, verifier, err := ac.RequestCode()
	if err != nil {
		log.Fatalf("authorize: %v", err)
	}

	fmt.Println()
	fmt.Println("  ┌───────────────────────────────────────────────────────┐")
	fmt.Println("  │  Press the ACTION button on your DIRIGERA hub now.    │")
	fmt.Println("  │  (small circle on the top of the hub)                 │")
	fmt.Println("  └───────────────────────────────────────────────────────┘")
	fmt.Println()
	fmt.Print("Press Enter here AFTER you've pressed the button > ")
	bufio.NewReader(os.Stdin).ReadString('\n')

	time.Sleep(500 * time.Millisecond)

	fmt.Println("Exchanging code for access token…")
	token, err := ac.ExchangeCode(code, verifier, *name)
	if err != nil {
		log.Fatalf("token exchange: %v\n\nIf the error mentions 'Button not pressed', try again — the exchange has to land within ~60 seconds of the authorize request.", err)
	}

	if err := writeToken(out, token); err != nil {
		log.Fatalf("write token: %v", err)
	}

	fmt.Printf("\n✓ Paired. Token written to %s (mode 0600).\n", out)
	fmt.Println("  This file contains a long-lived credential — keep it private.")
}

func writeToken(path, token string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(strings.TrimSpace(token) + "\n")
	return err
}

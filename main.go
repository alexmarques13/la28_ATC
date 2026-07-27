package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

func requireEnv(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", fmt.Errorf("Missing %s in .env (see .env.example)", name)
	}
	return value, nil
}

func main() {
	_ = godotenv.Load()

	target := flag.String("target", defaultTarget, "entry target URL")
	flag.Parse()

	code, err := run(*target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		if code == 0 {
			code = 1
		}
		os.Exit(code)
	}
	os.Exit(code)
}

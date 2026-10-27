package main

import (
	"fmt"
	"os"
	"time"

	"github.com/benipranata/zed-go-coverage/server/internal/lsp"
)

func main() {
	if err := lsp.New(os.Stdin, os.Stdout, time.Second).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "go-coverage-lsp:", err)
		os.Exit(1)
	}
}

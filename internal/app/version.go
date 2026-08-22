package app

import (
	"fmt"
	"io"
)

var Version = "0.1.0-dev"

func printVersion(w io.Writer) error {
	_, err := fmt.Fprintf(w, "aegis-agent v%s\n", Version)
	return err
}

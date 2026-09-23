// Command gencases regenerates the offline domain case pack under casepack/data.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"edgebench/casepack"
)

func main() {
	dir := filepath.Join("casepack", "data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, f := range casepack.GeneratedFiles() {
		p := filepath.Join(dir, f.Name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := os.WriteFile(p, append(f.Content, '\n'), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("wrote", p)
	}
}

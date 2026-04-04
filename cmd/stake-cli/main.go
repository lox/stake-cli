package main

import (
	"context"
	"fmt"
	"os"
)

func main() {
	if err := execute(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

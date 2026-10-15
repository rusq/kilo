// Command gokilo provides a simple editor (based on kilo.c).
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"

	"github.com/rusq/kilo"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("no filename specified")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := kilo.RunContext(ctx, os.Args[1]); err != nil {
		log.Fatalf("error running kilo: %s", err)
	}
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "Usage: %s <filename>", os.Args[0])
}

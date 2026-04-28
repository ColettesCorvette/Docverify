package main

import (
	"fmt"
	"log"
	"os"

	dc "docverify/internal/docverify"
)

func main() {
	fmt.Println("testclient: stub ready")
	fmt.Printf("Using model: %s\n", dc.DefaultModel)

	if len(os.Args) < 2 {
		log.Fatal("usage: testclient <path-to-document>")
	}

	path := os.Args[1]
	fmt.Printf("Would process: %s\n", path)

	// TODO: appeler dc.BuildInput, dc.EvaluateChecks, etc.
}

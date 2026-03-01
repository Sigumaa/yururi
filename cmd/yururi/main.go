package main

import (
	"flag"
	"log"
	"os"
)

func main() {
	configPath := flag.String("config", "runtime/config.yaml", "path to config yaml")
	flag.Parse()
	configureLogOutput(os.Stdout)

	if err := runApplication(*configPath); err != nil {
		log.Fatalf("yururi failed: %v", err)
	}
}

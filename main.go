package main

import (
	"log"

	"github.com/CloudNativeWorks/elchi-backend/cmd"
	"github.com/CloudNativeWorks/elchi-backend/pkg/version"
)

func main() {
	log.Printf("Envoy Version: %s", version.GetVersion())
	// go suubar.Start()
	cmd.Execute()
}

package main

import (
	"log"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/cmd"
	"github.com/CloudNativeWorks/elchi-backend/pkg/version"
	"github.com/go-acme/lego/v4/challenge/dns01"
)

func init() {
	log.Println("🚀 main.go init() started - configuring DNS resolvers...")

	// CRITICAL: Configure ACME DNS resolvers BEFORE any other package initialization
	// AddRecursiveNameservers and AddDNSTimeout return ChallengeOption functions
	// We need to execute them with a nil Challenge to update global variables
	nameserverOpt := dns01.AddRecursiveNameservers([]string{
		"208.67.222.222:53", // Open DNS Primary
		"1.1.1.1:53",        // Cloudflare DNS Primary
		"208.67.220.220:53", // Open DNS Secondary
		"1.0.0.1:53",        // Cloudflare DNS Secondary
	})
	_ = nameserverOpt(nil) // Execute option to set global recursiveNameservers

	timeoutOpt := dns01.AddDNSTimeout(3 * time.Second)
	_ = timeoutOpt(nil) // Execute option to set global dnsTimeout

	log.Println("✅ Initialized ACME DNS resolvers with public nameservers [8.8.8.8:53, 1.1.1.1:53, 8.8.4.4:53, 1.0.0.1:53]")
}

func main() {
	log.Printf("Envoy Version: %s", version.GetVersion())
	cmd.Execute()
}

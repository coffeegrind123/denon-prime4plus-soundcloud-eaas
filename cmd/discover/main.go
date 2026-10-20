package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/icedream/go-stagelinq/eaas"
)

func main() {
	timeout := flag.Duration("timeout", 15*time.Second, "Discovery duration")
	flag.Parse()

	discoverer, err := eaas.NewDiscoverer()
	if err != nil {
		log.Fatal(err)
	}
	defer discoverer.Shutdown()

	discoverer.ScanEvery(2 * time.Second)
	deadline := time.Now().Add(*timeout)
	seen := map[string]bool{}

	for time.Now().Before(deadline) {
		device, err := discoverer.Discover(time.Until(deadline))
		if err != nil {
			log.Printf("discover: %v", err)
			continue
		}
		if device == nil || seen[device.URL] {
			continue
		}
		seen[device.URL] = true
		fmt.Printf("name=%q url=%q version=%q\n", device.Hostname, device.URL, device.SoftwareVersion)
	}
	if len(seen) == 0 {
		log.Fatal("no EAAS sources discovered")
	}
}

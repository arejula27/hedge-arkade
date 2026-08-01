// Command waitstack blocks until arkd and the emulator can answer.
//
// A TCP connection is not the signal that matters: both ports open well before
// either service can say anything about itself. This connects properly and
// reads their GetInfo, which is what everything downstream needs anyway.
package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/arejula27/hedge/arkade"
)

func main() {
	budget := flag.Duration("timeout", 3*time.Minute, "how long to wait")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *budget)
	defer cancel()

	cfg := arkade.DefaultConfig()

	if err := arkade.WaitFor(ctx, cfg); err != nil {
		log.Fatalf("the stack never came up: %v", err)
	}

	stack, err := arkade.Connect(ctx, cfg)
	if err != nil {
		log.Fatalf("the stack is not answering: %v", err)
	}
	defer stack.Close()

	log.Printf("operator %x, emulator %x, exit delay %+v, dust %d",
		stack.ArkdSigner.SerializeCompressed(),
		stack.EmulatorSigner.SerializeCompressed(),
		stack.ExitDelay, stack.Dust)
}

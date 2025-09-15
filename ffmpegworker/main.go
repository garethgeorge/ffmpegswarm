package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/garethgeorge/ffmpegswarm/internal/ffmpegswarm"
)

var (
	workSlotsFlag = flag.Int("work-slots", 1, "Number of work slots for this worker")
)

func main() {
	flag.Parse()
	swarm, err := ffmpegswarm.NewFfmpegSwarm(0)
	if err != nil {
		fmt.Println("Error creating swarm worker:", err)
		return
	}
	swarm.SetWorkSlots(*workSlotsFlag)
	fmt.Println("Swarm worker listening on:")
	for _, addr := range swarm.Addresses() {
		fmt.Printf("\t- %s\n", addr)
	}

	mdnsStop := swarm.RunMdns()
	defer mdnsStop()

	if err := swarm.Serve(context.Background()); err != nil {
		fmt.Println("Error serving swarm worker:", err)
	}
	fmt.Println("Shutdown")
}

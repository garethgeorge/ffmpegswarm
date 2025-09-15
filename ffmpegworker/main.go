package main

import (
	"context"
	"fmt"

	"github.com/garethgeorge/ffmpegswarm/internal/ffmpegswarm"
)

func main() {
	swarm, err := ffmpegswarm.NewFfmpegSwarm(0)
	if err != nil {
		fmt.Println("Error creating swarm worker:", err)
		return
	}
	swarm.SetWorkSlots(1)
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

package main

import (
	"fmt"
	"os"
	"time"

	"github.com/garethgeorge/ffmpegswarm/internal/ffmpegswarm"
)

func main() {
	swarm, err := ffmpegswarm.NewFfmpegSwarm(0)
	if err != nil {
		fmt.Println("Error creating swarm worker:", err)
		return
	}
	swarm.SetWorkSlots(0)
	fmt.Println("Swarm client listening on:")
	for _, addr := range swarm.Addresses() {
		fmt.Printf("\t- %s\n", addr)
	}

	mdnsStop := swarm.RunMdns()
	defer mdnsStop()

	for {
		time.Sleep(1 * time.Second)
		peerID, err := swarm.PickPeer()
		if err != nil {
			fmt.Println("Error picking peer:", err)
			continue
		}
		fmt.Println("Picked peer:", peerID)

		// try to run the command on the remote peer
		if err := swarm.RunCommand(peerID, os.Args[1:], os.Stdout); err != nil {
			if err == ffmpegswarm.ErrPushback {
				fmt.Println("Worker pushback, trying again...")
				continue
			}
			fmt.Println("Error running command:", err)
		}
		return
	}
}

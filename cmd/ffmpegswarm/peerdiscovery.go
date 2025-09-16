package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/garethgeorge/ffmpegswarm/internal/ffmpegswarm"
)

func addPeers(swarm *ffmpegswarm.FfmpegSwarm) {
	if os.Getenv("FFMPEGSWARM_PEERS") == "" {
		mdnsStop := swarm.RunMdns()
		defer mdnsStop()
	} else {
		for _, peer := range strings.Split(os.Getenv("FFMPEGSWARM_PEERS"), ",") {
			if err := swarm.AddPeerToPeerstore(peer); err != nil {
				fmt.Println("Error adding peer to peerstore:", err)
			}
		}
	}
}

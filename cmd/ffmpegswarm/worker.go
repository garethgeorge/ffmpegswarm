package main

import (
	"context"
	"fmt"

	"github.com/garethgeorge/ffmpegswarm/internal/ffmpegswarm"
	"github.com/spf13/cobra"
)

var (
	workSlots int
	addrs     []string
)

var workerCmd = &cobra.Command{
	Use:   "worker",
	Short: "Run the ffmpegswarm worker",
	Run:   runWorker,
}

func init() {
	workerCmd.Flags().IntVarP(&workSlots, "work-slots", "n", 1, "Number of work slots for this worker")
	workerCmd.Flags().StringSliceVarP(&addrs, "addrs", "a", []string{"/ip4/0.0.0.0/tcp/0", "/ip6/::/tcp/0"}, "Addresses to listen on")
	rootCmd.AddCommand(workerCmd)
}

func runWorker(cmd *cobra.Command, args []string) {
	swarm, err := ffmpegswarm.NewFfmpegSwarm(addrs)
	if err != nil {
		fmt.Println("Error creating swarm worker:", err)
		return
	}
	swarm.SetWorkSlots(workSlots)
	fmt.Println("Swarm worker listening on:")
	for _, addr := range swarm.Addresses() {
		fmt.Printf("\t- %s\n", addr)
	}

	addPeers(swarm)
	if err := swarm.Serve(context.Background()); err != nil {
		fmt.Println("Error serving swarm worker:", err)
	}
	fmt.Println("Shutdown")
}

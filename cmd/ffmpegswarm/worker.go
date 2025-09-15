package main

import (
	"context"
	"fmt"

	"github.com/garethgeorge/ffmpegswarm/internal/ffmpegswarm"
	"github.com/spf13/cobra"
)

var (
	workSlots int
)

var workerCmd = &cobra.Command{
	Use:   "worker",
	Short: "Run the ffmpegswarm worker",
	Run:   runWorker,
}

func init() {
	workerCmd.Flags().IntVarP(&workSlots, "work-slots", "n", 1, "Number of work slots for this worker")
	rootCmd.AddCommand(workerCmd)
}

func runWorker(cmd *cobra.Command, args []string) {
	swarm, err := ffmpegswarm.NewFfmpegSwarm(0)
	if err != nil {
		fmt.Println("Error creating swarm worker:", err)
		return
	}
	swarm.SetWorkSlots(workSlots)
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

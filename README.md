# Ffmpeg Swarm

Ffmpegswarm allows for ffmpeg tasks to be executed across a pool of remote workers.

Worker discovery is currently implemented for LAN using mDNS. Clients will automatically connect to any workers found on the local network within a few seconds of joining. Clients poll available workers until a worker with an open work slot is available. Workslots are consumed by clients on a first come first serve basis.

## Usage

1. Run 1 or more ffmpegworkers:
```sh
ffmpegworker --work-slots 2
```

2. Execute a job
```sh
ffmpegclient -i input.mkv -c:v libx264 -t 120 output.mkv
```

## Future Work

 * Create a chunkedclient which splits input files into chunks to be processed in parallel
 * Implement a discovery and authentication mechanism for remote workers on non-LAN networks
 * Implement a cluster level status dashboard to show the status of all workers, what they're working on, etc.
 * Fair work queuing algorithm, right now tasks are assigned on a first come first serve basis. 
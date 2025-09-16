# Ffmpeg Swarm

Ffmpegswarm allows for ffmpeg tasks to be executed across a pool of remote workers.

Worker discovery is currently implemented for LAN using mDNS. Clients will automatically connect to any workers found on the local network within a few seconds of joining. Clients poll available workers until a worker with an open work slot is available. Workslots are consumed by clients on a first come first serve basis.

## Usage

1. Run 1 or more ffmpegworkers:
```sh
ffmpegswarm worker --work-slots 2 --addrs /ip4/127.0.0.1/tcp/54993
```

2. Execute a job
```sh
docker run -e FFMPEGSWARM_PEERS=/ip4/172.17.0.2/tcp/8080/p2p/12D3KooWMSpWhuC9mgpdfrenzEmcgAiCVViwCuZ3GnNbiGxsptCF -it garethgeorge/ffmpegswarm ffmpegswarm ffmpeg -i input.mkv -c:v libx264 -t 120 output.mkv
```

## Docker Usage

1. Run 1 or more ffmpegworkers:
```sh
docker run -p 8080 -it garethgeorge/ffmpegswarm ffmpegswarm --addrs=/ip4/0.0.0.0/tcp/8080 worker
```

2. Execute a job
```sh

```

## Future Work

 * Create a chunkedclient which splits input files into chunks to be processed in parallel
 * Implement a discovery and authentication mechanism for remote workers on non-LAN networks
 * Implement a cluster level status dashboard to show the status of all workers, what they're working on, etc.
 * Fair work queuing algorithm, right now tasks are assigned on a first come first serve basis. 
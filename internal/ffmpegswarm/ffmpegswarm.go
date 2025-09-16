package ffmpegswarm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	// We need to import libp2p's libraries that we use in this project.

	"github.com/garethgeorge/ffmpegswarm/internal/ioutil"
	chi "github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/libp2p/go-libp2p"
	gsNet "github.com/libp2p/go-libp2p-gostream"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	ma "github.com/multiformats/go-multiaddr"
	"golang.org/x/sync/errgroup"
)

var ErrPushback = errors.New("worker pushback, try another")

const addrPlaceholder = "[ADDR_PLACEHOLDER]"

// Protocol defines the libp2p protocol that we will use for the libp2p proxy
// service that we are going to provide. This will tag the streams used for
// this service. Streams are multiplexed and their protocol tag helps
// libp2p handle them to the right handler functions.
const Protocol = "/ffmpegswarm/0.0.1"

type FfmpegSwarm struct {
	host host.Host

	httpClient *http.Client

	cmdServerAddrCv    sync.Cond
	cmdServerAddrValue string

	tasksMu sync.Mutex
	tasks   map[string]*Task

	sharedFilesMu sync.Mutex
	sharedFiles   map[string]string // file ID -> local disk path

	workSlots int
}

func NewFfmpegSwarm(addrs []string) (*FfmpegSwarm, error) {
	options := []libp2p.Option{
		libp2p.ProtocolVersion(Protocol),
		libp2p.Security(noise.ID, noise.New),
	}
	if len(addrs) > 0 {
		options = append(options, libp2p.ListenAddrStrings(addrs...))
	} else {
		options = append(options, libp2p.NoListenAddrs)
	}
	host, err := libp2p.New(options...)
	if err != nil {
		return nil, fmt.Errorf("create libp2p host: %w", err)
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				peerStr := strings.Split(addr, ":")[0]
				peerID, err := peer.Decode(peerStr)
				if err != nil {
					return nil, fmt.Errorf("decode peer ID %s: %w", peerStr, err)
				}
				return gsNet.Dial(ctx, host, peerID, Protocol)
			},
		},
	}

	swarm := &FfmpegSwarm{
		host:        host,
		httpClient:  httpClient,
		workSlots:   1,
		tasks:       make(map[string]*Task),
		sharedFiles: make(map[string]string),
		cmdServerAddrCv: sync.Cond{
			L: &sync.Mutex{},
		},
	}
	return swarm, nil
}

func (f *FfmpegSwarm) SetWorkSlots(workSlots int) {
	f.workSlots = workSlots
}

func (f *FfmpegSwarm) RunMdns() func() {
	mdnsService := mdns.NewMdnsService(f.host, "ffmpegswarm", &discoveryNotifee{h: f.host})
	mdnsService.Start()
	return func() {
		mdnsService.Close()
	}
}

func (f *FfmpegSwarm) AddPeerToPeerstore(addr string) error {
	peeraddr, err := ma.NewMultiaddr(addr)
	if err != nil {
		return fmt.Errorf("parse multiaddr %q: %w", addr, err)
	}
	pidStr, err := peeraddr.ValueForProtocol(ma.P_P2P)
	if err != nil {
		return fmt.Errorf("create peer ID from multiaddr %q: %w", addr, err)
	}
	peerID, err := peer.Decode(pidStr)
	if err != nil {
		return fmt.Errorf("decode peer ID %s: %w", pidStr, err)
	}
	f.host.Peerstore().AddAddr(peerID, peeraddr, peerstore.PermanentAddrTTL)
	return nil
}

func (f *FfmpegSwarm) Addresses() []string {
	addresses := make([]string, 0, len(f.host.Addrs()))
	for _, a := range f.host.Addrs() {
		addresses = append(addresses, fmt.Sprintf("%s/p2p/%s", a, f.host.ID()))
	}
	return addresses
}

func (f *FfmpegSwarm) PickPeer() (peer.ID, error) {
	reqCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	peers := f.host.Peerstore().Peers()
	resultChan := make(chan peer.ID, len(peers))
	var wg sync.WaitGroup
	wg.Add(len(peers))
	for _, peerID := range peers {
		go func(peerID peer.ID) {
			defer wg.Done()
			if peerID == f.host.ID() {
				return
			}

			req, err := http.NewRequestWithContext(reqCtx, "GET", fmt.Sprintf("http://%s/v1/info", peerID.String()), nil)
			if err != nil {
				fmt.Printf("Error creating request for peer %s: %v\n", peerID, err)
				return
			}
			resp, err := f.httpClient.Do(req)
			if err != nil {
				fmt.Printf("Error getting info from peer %s: %v\n", peerID, err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				fmt.Printf("Error getting info from peer %s: %v\n", peerID, resp.Status)
				return
			}

			var workerState WorkerState
			if err := json.NewDecoder(resp.Body).Decode(&workerState); err != nil {
				fmt.Printf("Error decoding info from peer %s: %v\n", peerID, err)
				return
			}

			if workerState.WorkSlots > workerState.TaskCount {
				resultChan <- peerID
			}
		}(peerID)
	}
	wg.Wait()
	close(resultChan)

	var options []peer.ID
	for peerID := range resultChan {
		options = append(options, peerID)
	}
	if len(options) == 0 {
		return "", fmt.Errorf("no available peers")
	}

	return options[rand.Intn(len(options))], nil
}

// RunCommand runs a command on a remote peer, may return ErrPushback if the peer is too busy in which case the command should be retried elsewhere.
func (f *FfmpegSwarm) RunCommand(peerID peer.ID, args []string, output io.Writer) error {
	if len(args) < 1 {
		return fmt.Errorf("must provide at least 1 argument to ffmpeg")
	}

	replacePathLike := func(path string) (string, error) {
		// check that it's a valid path
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return "", fmt.Errorf("path %s does not exist", path)
		}

		return fmt.Sprintf("http://%s/v1/remotefile/%s/%s", addrPlaceholder, f.host.ID().String(), f.addSharedFile(path)), nil
	}

	createUploadForPathLike := func(path string) (string, error) {
		// check that it's a valid path
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			return "", fmt.Errorf("path %s already exists or is not accessible", path)
		}
		return fmt.Sprintf("http://%s/v1/remoteupload/%s/%s", addrPlaceholder, f.host.ID().String(), f.addSharedFile(path)), nil
	}

	for idx := 0; idx < len(args); idx++ {
		if args[idx] == "-i" {
			idx++
			var err error
			args[idx], err = replacePathLike(args[idx])
			if err != nil {
				return fmt.Errorf("path substitution failed: %w", err)
			}
		}
	}

	// assume by convention that the last thing on the path is the output
	var err error
	args[len(args)-1], err = createUploadForPathLike(args[len(args)-1])
	if err != nil {
		return fmt.Errorf("upload creation failed: %w", err)
	}

	log.Printf("Running command on peer %s: %v", peerID, args)

	task := Task{
		ID:   uuid.New().String(),
		Args: args,
	}

	payloadBuf := bytes.NewBuffer(nil)
	if err := json.NewEncoder(payloadBuf).Encode(task); err != nil {
		return fmt.Errorf("failed to encode task: %w", err)
	}

	resp, err := f.httpClient.Post(fmt.Sprintf("http://%s/v1/command", peerID.String()), "application/json", payloadBuf)
	if err != nil {
		return fmt.Errorf("failed to send task: %w", err)
	}
	defer resp.Body.Close()

	if output != nil {
		if _, err := io.Copy(output, resp.Body); err != nil {
			return fmt.Errorf("failed to copy output: %w", err)
		}
	} else {
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			return fmt.Errorf("failed to consume output: %w", err)
		}
	}

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusServiceUnavailable {
			return ErrPushback
		}
		return fmt.Errorf("failed to send task: %s", resp.Status)
	}

	return nil
}

func (f *FfmpegSwarm) Serve(ctx context.Context) error {
	var eg errgroup.Group
	eg.Go(func() error {
		mux := f.createPeerMux()
		server := &http.Server{
			Addr:    ":0",
			Handler: mux,
		}
		listener, err := gsNet.Listen(f.host, Protocol)
		if err != nil {
			return fmt.Errorf("failed to listen on libp2p: %w", err)
		}
		eg.Go(func() error {
			<-ctx.Done()
			return server.Shutdown(context.Background())
		})
		return server.Serve(listener)
	})

	eg.Go(func() error {
		mux := f.createCmdMux()
		server := &http.Server{
			Addr:    ":0",
			Handler: mux,
		}
		listener, err := net.Listen("tcp", ":0")
		if err != nil {
			return fmt.Errorf("failed to listen on tcp: %w", err)
		}
		server.Addr = listener.Addr().String()
		eg.Go(func() error {
			<-ctx.Done()
			return server.Shutdown(context.Background())
		})
		eg.Go(func() error {
			// Spin until the server is reachable
			start := time.Now()
			for {
				if time.Since(start) > 2*time.Second {
					return fmt.Errorf("timed out waiting for ffmpeg command api server to be reachable")
				}
				time.Sleep(50 * time.Millisecond)
				resp, err := http.Get(fmt.Sprintf("http://%s", server.Addr))
				if err == nil {
					resp.Body.Close()
					break
				}
			}
			f.cmdServerAddrCv.L.Lock()
			f.cmdServerAddrValue = server.Addr
			f.cmdServerAddrCv.L.Unlock()
			f.cmdServerAddrCv.Broadcast()
			return nil
		})

		return server.Serve(listener)
	})

	return eg.Wait()
}

func (f *FfmpegSwarm) getCmdServerAddr() string {
	f.cmdServerAddrCv.L.Lock()
	defer f.cmdServerAddrCv.L.Unlock()
	for f.cmdServerAddrValue == "" {
		f.cmdServerAddrCv.Wait()
	}
	return f.cmdServerAddrValue
}

func (f *FfmpegSwarm) addSharedFile(fpath string) string {
	f.sharedFilesMu.Lock()
	defer f.sharedFilesMu.Unlock()
	fid := uuid.New().String() + filepath.Ext(fpath)
	f.sharedFiles[fid] = fpath
	return fid
}

func (f *FfmpegSwarm) getSharedFile(fid string) string {
	f.sharedFilesMu.Lock()
	defer f.sharedFilesMu.Unlock()
	fpath, ok := f.sharedFiles[fid]
	if !ok {
		return ""
	}
	return fpath
}

// createMux provides functionality to peers
func (f *FfmpegSwarm) createPeerMux() http.Handler {
	r := chi.NewRouter()

	r.Get("/v1/info", func(w http.ResponseWriter, r *http.Request) {
		f.tasksMu.Lock()
		defer f.tasksMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(WorkerState{
			WorkSlots: f.workSlots,
			TaskCount: len(f.tasks),
		})
	})

	r.Post("/v1/command", func(w http.ResponseWriter, r *http.Request) {
		var task Task
		if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
			http.Error(w, fmt.Errorf("expected task: %w", err).Error(), http.StatusBadRequest)
			return
		}
		f.tasksMu.Lock()
		if len(f.tasks) >= f.workSlots {
			f.tasksMu.Unlock()
			http.Error(w, "too many tasks", http.StatusServiceUnavailable)
			return
		}

		f.tasks[task.ID] = &task
		f.tasksMu.Unlock()
		defer func() {
			f.tasksMu.Lock()
			delete(f.tasks, task.ID)
			f.tasksMu.Unlock()
		}()

		// Substitute the address into the command to be executed
		args := slices.Clone(task.Args)
		for idx := 0; idx < len(args); idx++ {
			args[idx] = strings.ReplaceAll(args[idx], addrPlaceholder, f.getCmdServerAddr())
		}
		log.Printf("running command: %v", args)

		// Exec ffmpeg with the task arguments, and pipe the stdout and stderr to the response writer
		cmd := exec.CommandContext(r.Context(), "ffmpeg", args...)

		tsrw := &ioutil.ThreadSafeResponseWriter{W: w}
		var output io.Writer = tsrw
		if f.workSlots <= 1 {
			output = io.MultiWriter(output, os.Stdout)
		}
		cmd.Stdout = output
		cmd.Stderr = output

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		ticker := time.NewTicker(time.Second * 1)
		defer ticker.Stop()
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					tsrw.Flush()
				}
			}
		}()
		if err := cmd.Run(); err != nil {
			log.Printf("ffmpeg failed: %v", err)
			return
		}
		log.Printf("ffmpeg completed successfully")
	})

	r.Get("/v1/file/{id}", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("serving file %s to remote peer %s", chi.URLParam(r, "id"), r.RemoteAddr)
		fileID := chi.URLParam(r, "id")
		if fileID == "" {
			http.Error(w, "missing file 'id' parameter", http.StatusBadRequest)
			return
		}

		filePath := f.getSharedFile(fileID)
		if filePath == "" {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}

		// Check if file exists on disk
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			http.Error(w, "file not found on disk", http.StatusNotFound)
			return
		}

		// Open and read the file
		file, err := os.Open(filePath)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to open file: %v", err), http.StatusInternalServerError)
			return
		}
		defer file.Close()

		// Guess MIME type based on file extension
		mimeType := mime.TypeByExtension(filepath.Ext(filePath))
		if mimeType == "" {
			mimeType = "application/octet-stream" // Default binary type
		}

		// Set content type header
		w.Header().Set("Content-Type", mimeType)
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filepath.Base(filePath)))

		// Copy file contents to response with buffered writer
		bufWriter := bufio.NewWriterSize(w, 32*1024) // 32KB buffer
		_, err = io.Copy(bufWriter, file)
		if err != nil {
			log.Printf("Error serving file %s: %v", filePath, err)
			return
		}
		if err = bufWriter.Flush(); err != nil {
			log.Printf("Error flushing buffer for file %s: %v", filePath, err)
		}
	})

	r.Post("/v1/upload/{id}", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("receiving uploaded file %s from peer %s", chi.URLParam(r, "id"), r.RemoteAddr)
		fileID := chi.URLParam(r, "id")
		if fileID == "" {
			http.Error(w, "missing file 'id' parameter", http.StatusBadRequest)
			return
		}

		// require an upload exists
		uploadPath := f.getSharedFile(fileID)
		if uploadPath == "" {
			http.Error(w, "upload not found", http.StatusNotFound)
			return
		}

		// write the file to disk with buffered writer
		file, err := os.Create(uploadPath)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to create file: %v", err), http.StatusInternalServerError)
			return
		}
		defer file.Close()

		bufWriter := bufio.NewWriterSize(file, 32*1024) // 32KB buffer
		_, err = io.Copy(bufWriter, r.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to write file: %v", err), http.StatusInternalServerError)
			return
		}
		if err = bufWriter.Flush(); err != nil {
			http.Error(w, fmt.Sprintf("failed to flush file buffer: %v", err), http.StatusInternalServerError)
			return
		}
	})

	return r
}

// createCmdMux provides functionality for local ffmpeg processes
func (f *FfmpegSwarm) createCmdMux() http.Handler {
	r := chi.NewRouter()
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r.Get("/v1/remotefile/{peer}/{id}", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("fetching a remote file %s from peer %s for a local ffmpeg process", chi.URLParam(r, "id"), chi.URLParam(r, "peer"))
		peer := chi.URLParam(r, "peer")
		id := chi.URLParam(r, "id")

		remoteURL := fmt.Sprintf("http://%s/v1/file/%s", peer, id)

		req, err := http.NewRequestWithContext(r.Context(), "GET", remoteURL, nil)
		if err != nil {
			log.Printf("failed to create request for peer %s: %v", peer, err)
			http.Error(w, "failed to create internal request", http.StatusInternalServerError)
			return
		}

		resp, err := f.httpClient.Do(req)
		if err != nil {
			log.Printf("failed to /v1/file request to peer %s: %v", peer, err)
			http.Error(w, fmt.Sprintf("failed to /v1/file request to peer %s", peer), http.StatusServiceUnavailable)
			return
		}
		defer resp.Body.Close()

		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(resp.StatusCode)
		// Use buffered writer for response proxying
		bufWriter := bufio.NewWriterSize(w, 32*1024) // 32KB buffer
		io.Copy(bufWriter, resp.Body)
		bufWriter.Flush()
	})

	r.Post("/v1/remoteupload/{peer}/{id}", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("uploading file %s from peer %s for a local ffmpeg process", chi.URLParam(r, "id"), chi.URLParam(r, "peer"))
		peer := chi.URLParam(r, "peer")
		id := chi.URLParam(r, "id")
		if id == "" {
			http.Error(w, "missing file 'id' parameter", http.StatusBadRequest)
			return
		}

		remoteURL := fmt.Sprintf("http://%s/v1/upload/%s", peer, id)
		req, err := http.NewRequestWithContext(context.Background(), "POST", remoteURL, r.Body)
		if err != nil {
			log.Printf("failed to create request for peer %s: %v", peer, err)
			http.Error(w, "failed to create internal request", http.StatusInternalServerError)
			return
		}
		resp, err := f.httpClient.Do(req)
		if err != nil {
			log.Printf("failed to send /v1/upload request to peer %s: %v", peer, err)
			http.Error(w, fmt.Sprintf("failed to send /v1/upload request to peer %s", peer), http.StatusServiceUnavailable)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		// Use buffered writer for upload response proxying
		bufWriter := bufio.NewWriterSize(w, 32*1024) // 32KB buffer
		io.Copy(bufWriter, resp.Body)
		bufWriter.Flush()
	})

	return r
}

type WorkerState struct {
	WorkSlots int `json:"work_slots"`
	TaskCount int `json:"task_count"`
}

type Task struct {
	ID   string   `json:"id"`
	Args []string `json:"args"`
}

// discoveryNotifee is a struct to be passed to mdns.NewMdnsService,
// and implements the mdns.Notifee interface.
type discoveryNotifee struct {
	h host.Host
}

// HandlePeerFound is called when a new peer is found via mDNS.
func (n *discoveryNotifee) HandlePeerFound(p peer.AddrInfo) {
	err := n.h.Connect(context.Background(), p)
	if err != nil {
		fmt.Printf("Discovered new peer but couldn't connect %s: %v\n", p.ID, err)
	}
	fmt.Printf("Connected to new peer: %s\n", p.ID)
}

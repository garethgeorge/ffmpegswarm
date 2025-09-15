package ffmpegswarm

import (
	"bytes"
	"context"
	"encoding/json"
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
	"strings"
	"sync"

	// We need to import libp2p's libraries that we use in this project.
	chi "github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/libp2p/go-libp2p"
	gsNet "github.com/libp2p/go-libp2p-gostream"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

// Protocol defines the libp2p protocol that we will use for the libp2p proxy
// service that we are going to provide. This will tag the streams used for
// this service. Streams are multiplexed and their protocol tag helps
// libp2p handle them to the right handler functions.
const Protocol = "/ffmpegswarm/0.0.1"

type FfmpegSwarm struct {
	host host.Host

	httpClient *http.Client

	tasksMu sync.Mutex
	tasks   map[string]*Task

	sharedFilesMu sync.Mutex
	sharedFiles   map[string]string // file ID -> local disk path

	uploadsMu sync.Mutex
	uploads   map[string]string // file ID -> local disk path (for uploads)
}

func NewFfmpegSwarm(port int) *FfmpegSwarm {
	host, err := libp2p.New(libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", port)))
	if err != nil {
		log.Fatalln(err)
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				peerStr := strings.Split(addr, ":")[0]
				peerID, err := peer.Decode(peerStr)
				if err != nil {
					return nil, fmt.Errorf("failed to decode peer ID %s: %w", peerStr, err)
				}
				return gsNet.Dial(ctx, host, peerID, Protocol)
			},
		},
	}

	return &FfmpegSwarm{
		host:       host,
		httpClient: httpClient,
	}
}

func (f *FfmpegSwarm) PickPeer() peer.ID {
	// pick a peer at random
	peers := f.host.Peerstore().Peers()
	return peers[rand.Intn(len(peers))]
}

func (f *FfmpegSwarm) RunCommand(peerID peer.ID, args []string, output io.Writer) error {
	replacePathLike := func(path string) (string, error) {
		// check that it's a valid path
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return "", fmt.Errorf("path %s does not exist", path)
		}

		f.sharedFilesMu.Lock()
		defer f.sharedFilesMu.Unlock()

		fileID := uuid.New().String() + filepath.Ext(path)
		f.sharedFiles[fileID] = path
		return fmt.Sprintf("http://localhost:8080/v1/remotefile/%s/%s", f.host.ID().String(), fileID), nil
	}

	createUploadForPathLike := func(path string) (string, error) {
		// check that it's a valid path
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			return "", fmt.Errorf("path %s already exists or is not accessible", path)
		}
		f.uploadsMu.Lock()
		defer f.uploadsMu.Unlock()
		uploadID := uuid.New().String() + filepath.Ext(path)
		f.uploads[uploadID] = path
		return fmt.Sprintf("http://localhost:8080/v1/upload/%s", uploadID), nil
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
		return fmt.Errorf("failed to send task: %s", resp.Status)
	}

	return nil
}

func (f *FfmpegSwarm) Serve() error {
	mux := f.createMux()
	listener, err := gsNet.Listen(f.host, Protocol)
	if err != nil {
		return fmt.Errorf("failed to listen on libp2p: %w", err)
	}
	return http.Serve(listener, mux)
}

func (f *FfmpegSwarm) createMux() http.Handler {
	r := chi.NewRouter()

	r.Get("/v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		f.tasksMu.Lock()
		defer f.tasksMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(f.tasks)
	})

	r.Post("/v1/command", func(w http.ResponseWriter, r *http.Request) {
		var task Task
		if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
			http.Error(w, fmt.Errorf("expected task: %w", err).Error(), http.StatusBadRequest)
			return
		}
		f.tasksMu.Lock()
		f.tasks[task.ID] = &task
		f.tasksMu.Unlock()
		defer func() {
			f.tasksMu.Lock()
			delete(f.tasks, task.ID)
			f.tasksMu.Unlock()
		}()

		// Exec ffmpeg with the task arguments, and pipe the stdout and stderr to the response writer
		cmd := exec.Command("ffmpeg", task.Args...)
		cmd.Stdout = w
		cmd.Stderr = w
		if err := cmd.Run(); err != nil {
			http.Error(w, fmt.Errorf("failed to run ffmpeg: %w", err).Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	r.Get("/v1/remotefile/{peer}/{id}", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("fetching a remote file %s from peer %s for a local ffmpeg process", chi.URLParam(r, "id"), chi.URLParam(r, "peer"))
		peer := chi.URLParam(r, "peer")
		id := chi.URLParam(r, "id")

		remoteURL := fmt.Sprintf("http://%s/v1/file/%s", peer, id)

		req, err := http.NewRequestWithContext(r.Context(), "GET", remoteURL, nil)
		if err != nil {
			log.Printf("Failed to create request for peer %s: %v", peer, err)
			http.Error(w, "Failed to create internal request", http.StatusInternalServerError)
			return
		}

		resp, err := f.httpClient.Do(req)
		if err != nil {
			log.Printf("Failed to send request to peer %s: %v", peer, err)
			http.Error(w, fmt.Sprintf("Failed to send request to peer %s", peer), http.StatusServiceUnavailable)
			return
		}
		defer resp.Body.Close()

		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})

	r.Get("/v1/file/{id}", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("serving file %s to remote peer %s", chi.URLParam(r, "id"), r.RemoteAddr)
		fileID := chi.URLParam(r, "id")
		if fileID == "" {
			http.Error(w, "missing file 'id' parameter", http.StatusBadRequest)
			return
		}

		f.sharedFilesMu.Lock()
		filePath, exists := f.sharedFiles[fileID]
		f.sharedFilesMu.Unlock()

		if !exists {
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

		// Copy file contents to response
		_, err = io.Copy(w, file)
		if err != nil {
			log.Printf("Error serving file %s: %v", filePath, err)
		}
	})

	r.Put("/v1/upload/{id}", func(w http.ResponseWriter, r *http.Request) {
		fileID := chi.URLParam(r, "id")
		if fileID == "" {
			http.Error(w, "missing file 'id' parameter", http.StatusBadRequest)
			return
		}

		// require an upload exists
		uploadPath, exists := f.uploads[fileID]
		if !exists {
			http.Error(w, "upload not found", http.StatusNotFound)
			return
		}

		// write the file to disk
		file, err := os.Create(uploadPath)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to create file: %v", err), http.StatusInternalServerError)
			return
		}
		defer file.Close()

		_, err = io.Copy(file, r.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to write file: %v", err), http.StatusInternalServerError)
			return
		}
	})

	return r
}

type Task struct {
	ID   string   `json:"id"`
	Args []string `json:"args"`
}

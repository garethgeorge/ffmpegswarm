package netutil

import (
	"net"
)

func AllocOpenAddr() (string, error) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return "", err
	}
	defer listener.Close()

	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return "", err
	}
	return "127.0.0.1:" + port, nil
}

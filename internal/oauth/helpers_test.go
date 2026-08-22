package oauth

import (
	"net"
	"strconv"
	"testing"
)

// freePort reserves an ephemeral port, releases it, and returns the number.
// Reusing it is inherently racy, but it is the standard way to obtain a
// plausible fixed port for a test that needs one.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// listenOn holds a port so a collision can be tested.
func listenOn(port int) (net.Listener, error) {
	return net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
}

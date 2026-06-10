// turn-test: test TURN relay CreatePermission to arbitrary IP.
// Takes TURN credentials and tries to relay a UDP packet through the TURN server.
//
// Usage:
//   go run . -server 155.212.205.174:19302 -user "1775775348:1125899954246530" -pass "loArGtxeNkgpjW184Fytibjj2XI=" -peer 158.160.248.141:56000
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v4"
)

func main() {
	server := flag.String("server", "", "TURN server address (host:port)")
	user := flag.String("user", "", "TURN username")
	pass := flag.String("pass", "", "TURN credential")
	peer := flag.String("peer", "158.160.248.141:56000", "Target peer address to test CreatePermission")
	flag.Parse()

	if *server == "" || *user == "" || *pass == "" {
		fmt.Println("Usage: go run . -server HOST:PORT -user USER -pass PASS [-peer IP:PORT]")
		os.Exit(1)
	}

	fmt.Printf("TURN server: %s\n", *server)
	fmt.Printf("Username:    %s\n", *user)
	fmt.Printf("Peer:        %s\n", *peer)
	fmt.Println()

	// Resolve TURN server
	turnAddr, err := net.ResolveUDPAddr("udp", *server)
	if err != nil {
		fatal(err, "resolve TURN server")
	}

	// Connect to TURN server
	fmt.Println("[1/4] Connecting to TURN server...")
	conn, err := net.DialUDP("udp", nil, turnAddr)
	if err != nil {
		fatal(err, "dial TURN server")
	}
	defer conn.Close()

	// Create TURN client
	fmt.Println("[2/4] Allocating relay...")
	lf := logging.NewDefaultLoggerFactory()
	lf.DefaultLogLevel = logging.LogLevelInfo

	client, err := turn.NewClient(&turn.ClientConfig{
		STUNServerAddr: *server,
		TURNServerAddr: *server,
		Username:       *user,
		Password:       *pass,
		Conn:           turn.NewSTUNConn(conn),
		LoggerFactory:  lf,
	})
	if err != nil {
		fatal(err, "create TURN client")
	}
	defer client.Close()

	err = client.Listen()
	if err != nil {
		fatal(err, "TURN listen")
	}

	// Allocate relay
	relayConn, err := client.Allocate()
	if err != nil {
		fatal(err, "TURN allocate")
	}
	defer relayConn.Close()

	relayAddr := relayConn.LocalAddr()
	fmt.Printf("    Relay address: %s\n", relayAddr)

	// Resolve peer
	peerAddr, err := net.ResolveUDPAddr("udp", *peer)
	if err != nil {
		fatal(err, "resolve peer")
	}

	// CreatePermission (implicit via SendTo)
	fmt.Printf("[3/4] CreatePermission to %s...\n", peerAddr)
	testData := []byte("TURNBRIDGE_PERMISSION_TEST")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Set deadline
	relayConn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err = relayConn.WriteTo(testData, peerAddr)
	if err != nil {
		fmt.Printf("\n=== FAILED ===\n")
		fmt.Printf("CreatePermission/Send FAILED: %v\n", err)
		fmt.Println("MAX TURN does NOT allow relaying to arbitrary IPs.")
		os.Exit(1)
	}

	fmt.Println("    Send succeeded!")

	// Try to receive response (optional — VPS may not respond, but send succeeding is the key test)
	fmt.Println("[4/4] Waiting for response (5s timeout, OK if none)...")
	buf := make([]byte, 1500)
	relayConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, from, err := relayConn.ReadFrom(buf)
	_ = ctx

	if err != nil {
		fmt.Printf("    No response (expected if VPS isn't echoing): %v\n", err)
	} else {
		fmt.Printf("    Got %d bytes from %s: %s\n", n, from, string(buf[:n]))
	}

	fmt.Println()
	fmt.Println("=== SUCCESS ===")
	fmt.Println("MAX TURN allows CreatePermission to arbitrary IPs!")
	fmt.Println("MAX provider is VIABLE for TurnBridge.")
}

func fatal(err error, context string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL [%s]: %v\n", context, err)
		os.Exit(1)
	}
}

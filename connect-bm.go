package main

import (
	"os"
	"fmt"
	"time"
	"net"
	"io"
	"io/ioutil"
)

const ConnectTimeEpsilon = 10 * time.Millisecond

func waitConnect(host string) (net.Conn, error) {
	for {
		conn, err := net.DialTimeout("tcp", host, ConnectTimeEpsilon)
		if err != nil {
			continue
		}
		return conn, nil
	}
}

func waitClose(conn net.Conn) error {
	defer conn.Close()
	if _, err := io.Copy(conn, os.Stdin); err != nil {
		return err
	}
	if _, err := io.Copy(ioutil.Discard, conn); err != nil {
		return err
	}
	return nil
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s HOST\n", os.Args[0])
		os.Exit(1)
	}
	t0 := time.Now()
	conn, err := waitConnect(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect test failed: %s\n", err)
	}
	tConn := time.Now()
	fmt.Printf("connect: %f secs\n", tConn.Sub(t0).Seconds())

	if err := waitClose(conn); err != nil {
		fmt.Fprintf(os.Stderr, "close test failed: %s\n", err)
	}
	tClose := time.Now()
	fmt.Printf("close:   %f secs\n", tClose.Sub(tConn).Seconds())
	fmt.Printf("total:   %f secs\n", tClose.Sub(t0).Seconds())
}

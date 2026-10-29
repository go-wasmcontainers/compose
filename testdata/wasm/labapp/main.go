//go:build wasip1

// Command labapp is the guest of compose's integration test, a stock Go
// wasip1 program using stealthrocket/net:
//
//	labapp write FILE TEXT   write TEXT to FILE (a volume), exit 0
//	labapp serve PORT FILE   serve FILE's content over HTTP on PORT
//	labapp get URL           GET URL (retrying while the server starts), print the body
//	labapp fail              exit 3
//	labapp probe URL         GET URL once
//	labapp lookup NAME       print how many addresses NAME resolves to
//	labapp env NAME          print NAME=value
//	labapp exists FILE       exit 0 if FILE exists, else 1 (a health check)
//	labapp ready-later MS FILE  write FILE after MS milliseconds, then run on
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/stealthrocket/net/wasip1"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: labapp write|serve|get|fail ...")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "exists":
		if _, err := os.Stat(os.Args[2]); err != nil {
			fmt.Println("not ready:", err)
			os.Exit(1)
		}
		fmt.Println("ready")
	case "ready-later":
		var ms int
		fmt.Sscan(os.Args[2], &ms)
		fmt.Println("getting ready")
		time.Sleep(time.Duration(ms) * time.Millisecond)
		if err := os.WriteFile(os.Args[3], []byte("ready\n"), 0o644); err != nil {
			fail(err)
		}
		fmt.Println("ready")
		for {
			time.Sleep(time.Hour)
		}
	case "write":
		if err := os.WriteFile(os.Args[2], []byte(os.Args[3]), 0o644); err != nil {
			fail(err)
		}
		fmt.Println("wrote", os.Args[2])
	case "serve":
		l, err := wasip1.Listen("tcp", ":"+os.Args[2])
		if err != nil {
			fail(err)
		}
		fmt.Println("listening on", os.Args[2])
		fail(http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, err := os.ReadFile(os.Args[3])
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			fmt.Fprintf(w, "%s (greeting %s)", b, os.Getenv("GREETING"))
		})))
	case "get", "probe":
		// get retries while the server starts; probe tries once.
		c := &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return wasip1.DialContext(ctx, network, addr)
			},
		}, Timeout: 5 * time.Second}
		tries := 100
		if os.Args[1] == "probe" {
			tries = 1
		}
		var last error
		for i := 0; i < tries; i++ {
			resp, err := c.Get(os.Args[2])
			if err == nil {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				fmt.Printf("got: %s\n", b)
				return
			}
			last = err
			time.Sleep(100 * time.Millisecond)
		}
		fail(last)
	case "lookup":
		// The addresses a name resolves to, through the network's DNS
		// (stealthrocket/net points Go's resolver at it).
		var addrs []string
		var err error
		for i := 0; i < 50; i++ {
			if addrs, err = net.DefaultResolver.LookupHost(context.Background(), os.Args[2]); err == nil && len(addrs) > 0 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			fail(err)
		}
		fmt.Printf("%s has %d addresses\n", os.Args[2], len(addrs))
	case "env":
		fmt.Printf("%s=%s\n", os.Args[2], os.Getenv(os.Args[2]))
	case "fail":
		fmt.Println("failing on purpose")
		os.Exit(3)
	default:
		fail(fmt.Errorf("unknown command %q", os.Args[1]))
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "labapp:", err)
	os.Exit(1)
}

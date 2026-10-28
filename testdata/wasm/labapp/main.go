//go:build wasip1

// Command labapp is the guest of compose's integration test, a stock Go
// wasip1 program using stealthrocket/net:
//
//	labapp write FILE TEXT   write TEXT to FILE (a volume), exit 0
//	labapp serve PORT FILE   serve FILE's content over HTTP on PORT
//	labapp get URL           GET URL (retrying while the server starts), print the body
//	labapp fail              exit 3
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
	case "get":
		c := &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return wasip1.DialContext(ctx, network, addr)
			},
		}, Timeout: 5 * time.Second}
		var last error
		for i := 0; i < 100; i++ {
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

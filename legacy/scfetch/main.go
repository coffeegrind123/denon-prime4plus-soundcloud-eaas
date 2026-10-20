// scfetch is a minimal static HTTPS fetcher for Engine OS, whose busybox
// wget is built without TLS.  It accepts the subset of wget flags used by
// soundcloud-public.sh:
//
//	scfetch -q -O out -U "user agent" --header='Name: value' URL
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	var (
		quiet  = flag.Bool("q", false, "quiet")
		out    = flag.String("O", "", "output file")
		ua     = flag.String("U", "", "user agent")
		header = flag.String("header", "", "extra header")
	)
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: scfetch [-q] [-O file] [-U ua] [--header 'K: V'] URL")
		os.Exit(2)
	}
	_ = quiet

	req, err := http.NewRequest(http.MethodGet, flag.Arg(0), nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *ua != "" {
		req.Header.Set("User-Agent", *ua)
	}
	if *header != "" {
		parts := strings.SplitN(*header, ":", 2)
		if len(parts) == 2 {
			req.Header.Set(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
		}
	}
	if req.Header.Get("Accept-Encoding") == "" {
		req.Header.Set("Accept-Encoding", "identity")
	}

	client := &http.Client{Timeout: 40 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "HTTP %d for %s\n", resp.StatusCode, flag.Arg(0))
		os.Exit(1)
	}

	var dst io.Writer = os.Stdout
	var f *os.File
	if *out != "" {
		f, err = os.Create(*out)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer f.Close()
		dst = f
	}
	if _, err := io.Copy(dst, resp.Body); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os/exec"
	"runtime"
	"strings"

	"github.com/pastorenue/kflow/cmd/kflow/uiassets"
	"github.com/spf13/cobra"
)

var uiCmd = &cobra.Command{
	Use:   "ui [port]",
	Short: "Serve the kflow dashboard locally and open it in the browser",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		listenAddr := ":0" // OS-assigned free port by default
		if len(args) == 1 {
			listenAddr = ":" + args[0]
		}

		ln, err := net.Listen("tcp", listenAddr)
		if err != nil {
			return fmt.Errorf("cannot bind on %s (port already in use?): %w", listenAddr, err)
		}

		port := ln.Addr().(*net.TCPAddr).Port

		token := loadSavedToken()
		if token == "" {
			key := resolveAPIKey(cmd)
			if key != "" {
				result, err := doJSONNoAuth("POST", "/api/v1/auth/token", map[string]string{"api_key": key})
				if err != nil {
					return fmt.Errorf("exchange api key for token: %w", err)
				}
				token, _ = result["token"].(string)
			}
		}

		targetURL := fmt.Sprintf("http://localhost:%d/", port)
		if token != "" {
			targetURL = fmt.Sprintf("http://localhost:%d/?token=%s", port, token)
		}

		orchestratorURL, err := url.Parse(serverFlag)
		if err != nil {
			return fmt.Errorf("invalid server URL: %w", err)
		}

		proxy := httputil.NewSingleHostReverseProxy(orchestratorURL)
		staticFiles := http.FileServer(http.FS(uiassets.FS))

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				proxy.ServeHTTP(w, r)
				return
			}
			// SPA fallback: if the path doesn't match a real asset, serve index.html
			// so the client-side router handles it.
			path := strings.TrimPrefix(r.URL.Path, "/")
			if path == "" {
				path = "index.html"
			}
			f, err := uiassets.FS.Open(path)
			if err != nil {
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/"
				staticFiles.ServeHTTP(w, r2)
				return
			}
			f.Close()
			staticFiles.ServeHTTP(w, r)
		})

		go func() {
			if err := http.Serve(ln, handler); err != nil {
				fmt.Printf("ui server error: %v\n", err)
			}
		}()

		fmt.Printf("Proxying /api/ → %s\n", serverFlag)
		fmt.Printf("Dashboard: %s\n", targetURL)
		openBrowser(targetURL)

		select {}
	},
}

func openBrowser(browserURL string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{browserURL}
	case "windows":
		cmd = "cmd"
		args = []string{"/c", "start", browserURL}
	default:
		cmd = "xdg-open"
		args = []string{browserURL}
	}
	_ = exec.Command(cmd, args...).Start()
}

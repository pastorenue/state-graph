package main

import (
	"fmt"
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
	Use:   "ui <port>",
	Short: "Serve the kflow dashboard locally and open it in the browser",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		port := args[0]

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

		targetURL := fmt.Sprintf("http://localhost:%s/", port)
		if token != "" {
			targetURL = fmt.Sprintf("http://localhost:%s/?token=%s", port, token)
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
			staticFiles.ServeHTTP(w, r)
		})

		go func() {
			if err := http.ListenAndServe(":"+port, handler); err != nil {
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

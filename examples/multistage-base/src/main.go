// The runtime of the multistage example: a small HTTP server that reports what the build put into the image.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"time"
)

var version = "dev"

func main() {
	listen := flag.String("listen", ":8080", "address to listen on")
	flag.Parse()
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		u, _ := user.Current()
		wd, _ := os.Getwd()
		fmt.Fprintf(w, "multistage example %s\nuser=%s wd=%s tz=%s now=%s\nconfig=%s assets=%s\n",
			version, u.Username, wd, os.Getenv("TZ"), time.Now().Format(time.RFC3339), os.Getenv("APP_CONFIG"), os.Getenv("APP_ASSETS"))
	})
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	http.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		raw, err := os.ReadFile(os.Getenv("APP_CONFIG"))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		var v any
		_ = json.Unmarshal(raw, &v)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	})
	http.HandleFunc("/assets/", func(w http.ResponseWriter, r *http.Request) {
		http.StripPrefix("/assets/", http.FileServer(http.Dir(os.Getenv("APP_ASSETS")))).ServeHTTP(w, r)
	})
	http.HandleFunc("/tree", func(w http.ResponseWriter, r *http.Request) {
		for _, root := range []string{os.Getenv("APP_ASSETS"), filepath.Dir(os.Getenv("APP_CONFIG"))} {
			_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
				if err == nil {
					fmt.Fprintf(w, "%s %o %d\n", p, info.Mode().Perm(), info.Size())
				}
				return nil
			})
		}
	})
	fmt.Println("listening on", *listen)
	if err := http.ListenAndServe(*listen, nil); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

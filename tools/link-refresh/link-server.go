package main

import (
	"flag"
	"log"
	"net/http"
	"os"
)

func main() {
	listen := flag.String("listen", "10.77.77.1:8080", "Listen address (WG interface)")
	linksFile := flag.String("links", "/home/ilya/link-refresh/links.json", "Path to links.json")
	flag.Parse()

	http.HandleFunc("/links", func(w http.ResponseWriter, r *http.Request) {
		data, err := os.ReadFile(*linksFile)
		if err != nil {
			http.Error(w, "links not available", http.StatusServiceUnavailable)
			log.Printf("read error: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
		log.Printf("served links to %s", r.RemoteAddr)
	})

	log.Printf("link-server listening on %s", *listen)
	if err := http.ListenAndServe(*listen, nil); err != nil {
		log.Fatal(err)
	}
}

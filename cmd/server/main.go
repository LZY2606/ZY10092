// Command server runs the tile-stitching registry with its browser UI.
package main

import (
	"flag"
	"log"
	"net/http"

	"gsb/internal/app"
	"gsb/internal/web"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:5218", "address to listen on")
	dataDir := flag.String("data", ".gsb-data", "persistence directory")
	flag.Parse()

	a, err := app.New(*dataDir)
	if err != nil {
		log.Fatalf("open app: %v", err)
	}
	srv := web.New(a)
	log.Printf("listening on http://%s (data: %s)", *listen, *dataDir)
	if err := http.ListenAndServe(*listen, srv.Mux); err != nil {
		log.Fatal(err)
	}
}

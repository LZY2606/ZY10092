package main

import (
	"flag"
	"log"
	"net/http"

	"tileforge/internal/api"
	"tileforge/internal/service"
	"tileforge/internal/store"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:5218", "HTTP listen address")
	dataDir := flag.String("data", ".tileforge-data", "persistent data directory")
	flag.Parse()

	st, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	svc := service.New(st)
	for _, record := range svc.RecoverInterrupted() {
		log.Printf("recovered interrupted build %s: %s", record.BuildID, record.Status)
	}

	server := &http.Server{Addr: *listen, Handler: api.New(svc)}
	log.Printf("TileForge listening on http://%s", *listen)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// Command unbound-dash serves a web dashboard for monitoring and
// controlling a local Unbound recursive DNS resolver.
package main

import (
	"context"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/airkingbr/unbound-dash/internal/api"
	"github.com/airkingbr/unbound-dash/internal/config"
	"github.com/airkingbr/unbound-dash/internal/isptools"
	"github.com/airkingbr/unbound-dash/internal/querylog"
	"github.com/airkingbr/unbound-dash/internal/unboundctl"
	"github.com/airkingbr/unbound-dash/web"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "/etc/unbound-dash/config.json", "path to config.json")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		log.Println(version)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

	static, err := fs.Sub(web.Static, "static")
	if err != nil {
		log.Fatalf("loading embedded assets: %v", err)
	}

	client := unboundctl.New(cfg.UnboundControlBin, cfg.UnboundConf)

	var tailer *querylog.Tailer
	if cfg.UnboundLogFile != "" {
		if _, err := os.Stat(cfg.UnboundLogFile); err == nil {
			tailer = querylog.NewTailer(cfg.UnboundLogFile)
			go tailer.Run()
		} else {
			log.Printf("query log %s not found, top domains/clients disabled: %v", cfg.UnboundLogFile, err)
		}
	}

	syncer := isptools.NewSyncer(isptools.Options{
		Client:          isptools.NewClient(cfg.ISPToolsAPIURL, cfg.ISPToolsToken, "unbound-dash/"+version),
		Ctl:             client,
		CheckconfBin:    cfg.UnboundCheckconf,
		UnboundConf:     cfg.UnboundConf,
		BlocklistFile:   cfg.ISPToolsBlocklistFile,
		LegacyFile:      cfg.LegacyBlocklistFile,
		StaticEntryFile: cfg.StaticEntryFile,
		StateDir:        cfg.StateDir,
		Interval:        time.Duration(cfg.ISPToolsSyncMinutes) * time.Minute,
	})
	go syncer.Run(context.Background())

	server := api.New(cfg, client, static, tailer, syncer, version)

	log.Printf("unbound-dash listening on %s", cfg.ListenAddr)
	if err := http.ListenAndServe(cfg.ListenAddr, server.Routes()); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

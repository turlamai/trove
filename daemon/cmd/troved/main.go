// troved is the Trove daemon: a root-owned background engine that performs
// storage-aware file operations for the Trove Unraid plugin. It listens on a
// local Unix socket only — never TCP — and authenticates the PHP gateway by
// socket peer credentials plus a per-start token file.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/turlamai/trove/daemon/internal/api"
	"github.com/turlamai/trove/daemon/internal/config"
	"github.com/turlamai/trove/daemon/internal/executor"
	"github.com/turlamai/trove/daemon/internal/jobs"
	"github.com/turlamai/trove/daemon/internal/model"
	"github.com/turlamai/trove/daemon/internal/planner"
	"github.com/turlamai/trove/daemon/internal/resolver"
	"github.com/turlamai/trove/daemon/internal/roots"
	"github.com/turlamai/trove/daemon/internal/unraid"
)

var version = "dev"

func main() {
	var (
		socketPath  = flag.String("socket", "/run/trove/troved.sock", "unix socket path")
		tokenPath   = flag.String("token-file", "/run/trove/token", "gateway token file (0600)")
		cfgPath     = flag.String("config", "/boot/config/plugins/trove/trove.cfg", "settings file")
		varIniPath  = flag.String("var-ini", "/var/local/emhttp/var.ini", "emhttp var.ini path")
		dataDirFlag = flag.String("data-dir", "", "override data directory (testing)")
		rootBase    = flag.String("dev-roots", "", "dev mode: base dir for synthetic roots")
		devTCP      = flag.String("dev-tcp", "", "dev mode: additionally serve plain HTTP on this TCP addr (localhost only, no auth)")
		showVersion = flag.Bool("version", false, "print version")
	)
	flag.Parse()
	if *showVersion {
		fmt.Println("troved", version)
		return
	}
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[troved] ")

	dev := *rootBase != "" || *devTCP != ""
	if !dev && os.Geteuid() != 0 {
		log.Fatal("troved must run as root on Unraid (or use --dev-roots/--dev-tcp for development)")
	}

	cfg := config.Load(*cfgPath)
	if *dataDirFlag != "" {
		cfg.DataDir = *dataDirFlag
	}

	// Socket directory + per-start gateway token.
	if err := os.MkdirAll(filepath.Dir(*socketPath), 0700); err != nil {
		log.Fatalf("socket dir: %v", err)
	}
	_ = os.Remove(*socketPath)
	token := genToken()
	if err := os.WriteFile(*tokenPath, []byte(token), 0600); err != nil {
		log.Fatalf("token file: %v", err)
	}

	// Registry + resolver.
	var reg *roots.Registry
	if *rootBase != "" {
		reg = devRegistry(*rootBase)
		log.Printf("DEV MODE: synthetic roots under %s — not for production", *rootBase)
	} else {
		reg = roots.New()
	}
	if len(reg.List()) == 0 {
		log.Printf("warning: no storage roots discovered (array stopped or dev mode misconfigured)")
	}
	res := resolver.New(reg)

	// Job engine. Without a data directory the daemon degrades to read-only
	// browsing (fail closed, spec T20).
	var manager *jobs.Manager
	var store *jobs.Store
	exec := executor.New(res)
	dataOK := false
	if cfg.DataDir != "" {
		jdir := filepath.Join(cfg.DataDir, "trove-jobs")
		if st, err := jobs.Open(jdir); err == nil {
			store = st
			manager = jobs.NewManager(exec, store, cfg.Concurrency)
			if err := manager.LoadState(); err != nil {
				log.Printf("journal load: %v (starting fresh)", err)
			}
			manager.Prune()
			dataOK = true
		} else {
			log.Printf("data dir %s unavailable (%v): read-only mode", jdir, err)
		}
	} else {
		log.Printf("no data directory configured: read-only mode")
	}

	plnr := planner.New(res, reg, func() planner.Settings {
		c := config.Load(*cfgPath)
		return planner.Settings{
			PhysicalWrite: c.PhysicalWrite,
			ProtectedMode: c.ProtectedMode,
			Trash:         c.Trash,
			DataDirOK:     dataOK,
			Concurrency:   c.Concurrency,
		}
	})

	api.Version = version
	api.StartedAt = time.Now()

	dep := api.Deps{
		Resolver: res,
		Registry: reg,
		Planner:  plnr,
		Manager:  manager,
		Executor: exec,
		Cfg: func() *config.Config {
			return config.Load(*cfgPath)
		},
		Uraid: func() unraid.State {
			return unraid.ReadVarIni(*varIniPath)
		},
		Token: token,
	}
	handler := api.New(dep)

	ln, err := listenUnixVerified(*socketPath)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	}()
	log.Printf("troved %s: socket %s, roots %d, jobs %s, concurrency %d",
		version, *socketPath, len(reg.List()), onOff(dataOK), cfg.Concurrency)

	if *devTCP != "" {
		go func() {
			log.Printf("DEV MODE: plain HTTP on http://%s — localhost development only", *devTCP)
			if err := http.ListenAndServe(*devTCP, handler); err != nil {
				log.Printf("dev tcp: %v", err)
			}
		}()
	}

	// Graceful shutdown on SIGTERM/SIGINT: stop accepting, let running jobs
	// finish (bounded), persist, remove socket.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	log.Printf("shutting down…")
	if manager != nil {
		manager.Close()
	}
	if store != nil {
		_ = store.Close()
	}
	_ = srv.Close()
	_ = os.Remove(*socketPath)
	log.Printf("bye")
}

func onOff(b bool) string {
	if b {
		return "enabled"
	}
	return "read-only"
}

func genToken() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// devRegistry registers synthetic roots for local UI development: one
// "share"-kind root per first-level directory of base, so the whole UI can
// be exercised on a macOS/Linux dev machine without Unraid mounts.
func devRegistry(base string) *roots.Registry {
	entries, err := os.ReadDir(base)
	var list []*roots.Root
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() || len(list) >= 8 {
				continue
			}
			list = append(list, &roots.Root{
				Ref:  model.KindShare,
				ID:   e.Name(),
				Path: filepath.Join(base, e.Name()),
			})
		}
	}
	return roots.NewWithRoots(list)
}

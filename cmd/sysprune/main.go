package main

import (
	"os"

	"github.com/cmerk2021/sysprune/internal/app"
)

// Injected by the linker (see .goreleaser.yaml).
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	os.Exit(app.Main(os.Args[1:], app.BuildInfo{Version: version, Commit: commit, Date: date}))
}

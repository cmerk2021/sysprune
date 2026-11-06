package apply

import (
	"github.com/cmerk2021/sysprune/data"
	"github.com/cmerk2021/sysprune/internal/startup"
)

// loadEmbeddedKB loads only the compiled-in knowledge base — the privileged
// helper never reads user-writable KB overrides.
func loadEmbeddedKB() (*startup.KB, error) {
	return startup.LoadKB(data.KBYaml, nil, "")
}

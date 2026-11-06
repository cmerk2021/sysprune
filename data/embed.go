// Package data embeds the built-in knowledge base.
package data

import _ "embed"

//go:embed kb.yaml
var KBYaml []byte

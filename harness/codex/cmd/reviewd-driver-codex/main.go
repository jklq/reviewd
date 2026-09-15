package main

import (
	"github.com/jklq/reviewd/harness/codex"
	"github.com/jklq/reviewd/harness/plugin"
)

func main() { plugin.Serve("github.com/jklq/reviewd/harness/codex", codex.Driver{}) }

package main

import (
	"github.com/jklq/reviewd/harness/muse"
	"github.com/jklq/reviewd/harness/plugin"
)

func main() { plugin.Serve("github.com/jklq/reviewd/harness/muse", muse.Driver{}) }

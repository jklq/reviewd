package main

import (
	"github.com/jklq/reviewd/harness/opencode"
	"github.com/jklq/reviewd/harness/plugin"
)

func main() { plugin.Serve("github.com/jklq/reviewd/harness/opencode", opencode.Driver{}) }

package main

import (
	"github.com/jklq/reviewd/harness/claudecode"
	"github.com/jklq/reviewd/harness/plugin"
)

func main() { plugin.Serve("github.com/jklq/reviewd/harness/claudecode", claudecode.Driver{}) }

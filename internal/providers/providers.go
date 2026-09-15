// Package providers links the official harness drivers into reviewd.
package providers

import (
	_ "github.com/jklq/reviewd/harness/claudecode"
	_ "github.com/jklq/reviewd/harness/codex"
	_ "github.com/jklq/reviewd/harness/muse"
	_ "github.com/jklq/reviewd/harness/opencode"
)

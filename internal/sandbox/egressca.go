package sandbox

import (
	"bytes"
	"encoding/pem"
	"fmt"
	"os"
)

func SplitCABundle(path string) ([]byte, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	var certs bytes.Buffer
	hasKey := false
	for {
		var block *pem.Block
		block, raw = pem.Decode(raw)
		if block == nil {
			break
		}
		switch block.Type {
		case "CERTIFICATE":
			if err := pem.Encode(&certs, block); err != nil {
				return nil, false, err
			}
		case "PRIVATE KEY", "RSA PRIVATE KEY", "EC PRIVATE KEY":
			hasKey = true
		}
	}
	if certs.Len() == 0 {
		return nil, false, fmt.Errorf("no CA certificate found in %s", path)
	}
	return certs.Bytes(), hasKey, nil
}

package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
)

// CatalogVersion must be incremented whenever a model-facing MCP tool name,
// input/output contract, or tool-selection semantic changes. It lets operators
// distinguish a stale client-side tools/list cache from an old MCPD binary.
const CatalogVersion = 8

func CatalogFingerprint() string {
	names := make([]string, 0, len(toolScopes))
	for name, scope := range toolScopes {
		names = append(names, name+":"+scope)
	}
	sort.Strings(names)
	material := "catalog-version=" + strconv.Itoa(CatalogVersion) + "\n" + strings.Join(names, "\n")
	sum := sha256.Sum256([]byte(material))
	return "sha256:" + hex.EncodeToString(sum[:])
}

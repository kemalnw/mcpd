package oauth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const maxMCPRequestBodyBytes = 4 << 20

type mcpRequestMethod struct {
	Method string
	Source string
}

// resolveMCPRequestMethod treats the JSON-RPC body as authoritative. Mcp-Method
// is only a compatibility hint: when both are present they must agree.
func resolveMCPRequestMethod(r *http.Request) (mcpRequestMethod, error) {
	headerMethod := r.Header.Get("Mcp-Method")
	if r.Body == nil {
		if headerMethod != "" {
			return mcpRequestMethod{Method: headerMethod, Source: "header"}, nil
		}
		return mcpRequestMethod{Source: "none"}, nil
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxMCPRequestBodyBytes+1))
	if err != nil {
		return mcpRequestMethod{}, fmt.Errorf("read MCP request body: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if len(body) > maxMCPRequestBodyBytes {
		return mcpRequestMethod{}, fmt.Errorf("MCP request body exceeds %d bytes", maxMCPRequestBodyBytes)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		if headerMethod != "" {
			return mcpRequestMethod{Method: headerMethod, Source: "header"}, nil
		}
		return mcpRequestMethod{Source: "none"}, nil
	}

	var envelope struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return mcpRequestMethod{}, fmt.Errorf("decode MCP JSON-RPC method: %w", err)
	}
	if headerMethod != "" && envelope.Method != "" && headerMethod != envelope.Method {
		return mcpRequestMethod{}, fmt.Errorf("Mcp-Method header %q disagrees with JSON-RPC method %q", headerMethod, envelope.Method)
	}
	if envelope.Method != "" {
		source := "body"
		if headerMethod != "" {
			source = "header+body"
		}
		return mcpRequestMethod{Method: envelope.Method, Source: source}, nil
	}
	if headerMethod != "" {
		return mcpRequestMethod{Method: headerMethod, Source: "header"}, nil
	}
	return mcpRequestMethod{Source: "body"}, nil
}

package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type identityContextKey struct{}

func IdentityFromContext(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(identityContextKey{}).(Identity)
	return identity, ok
}

type ScopeResolver func(toolName string) string

func ProtectMCP(next http.Handler, server *Server) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestMethod, err := resolveMCPRequestMethod(r)
		if err != nil {
			server.logger.Warn("mcp auth request rejected", "reason", "invalid_jsonrpc_request", "error", err)
			http.Error(w, "invalid MCP request", http.StatusBadRequest)
			return
		}
		method := requestMethod.Method
		if method == "server/discover" || method == "tools/list" {
			server.logger.Info("mcp auth decision", "jsonrpc_method", method, "method_source", requestMethod.Source, "decision", "allow_discovery")
			next.ServeHTTP(w, r)
			return
		}

		raw, present := bearerToken(r.Header.Get("Authorization"))
		if method == "tools/call" {
			// Tool-level auth failures must reach the MCP middleware so ChatGPT can
			// receive _meta["mcp/www_authenticate"] and launch its linking UI.
			if present {
				identity, verifyErr := server.VerifyBearer(raw, "")
				if verifyErr == nil {
					server.logger.Info("mcp auth decision", "jsonrpc_method", method, "method_source", requestMethod.Source, "auth_present", true, "auth_valid", true, "client_id", identity.ClientID, "decision", "defer_tool_scope_check")
					r = r.WithContext(context.WithValue(r.Context(), identityContextKey{}, identity))
				} else {
					server.logger.Warn("mcp auth decision", "jsonrpc_method", method, "method_source", requestMethod.Source, "auth_present", true, "auth_valid", false, "decision", "defer_tool_challenge", "reason", verifyErr.Error())
				}
			} else {
				server.logger.Info("mcp auth decision", "jsonrpc_method", method, "method_source", requestMethod.Source, "auth_present", false, "decision", "defer_tool_challenge")
			}
			next.ServeHTTP(w, r)
			return
		}

		if taskScope := taskMethodScope(method); taskScope != "" {
			if !present {
				server.logger.Info("mcp auth decision", "jsonrpc_method", method, "method_source", requestMethod.Source, "auth_present", false, "decision", "challenge", "required_scope", taskScope)
				writeBearerChallenge(w, server.ResourceMetadataURL(), taskScope, http.StatusUnauthorized, "", "")
				return
			}
			identity, verifyErr := server.VerifyBearer(raw, "")
			if verifyErr != nil {
				server.logger.Warn("mcp auth decision", "jsonrpc_method", method, "method_source", requestMethod.Source, "auth_present", true, "auth_valid", false, "decision", "challenge", "reason", verifyErr.Error())
				writeBearerChallenge(w, server.ResourceMetadataURL(), taskScope, http.StatusUnauthorized, "invalid_token", verifyErr.Error())
				return
			}
			allowed := false
			if method == "tasks/get" {
				_, hasRead := identity.Scopes[ScopeRead]
				_, hasWrite := identity.Scopes[ScopeWrite]
				allowed = hasRead || hasWrite
			} else {
				_, allowed = identity.Scopes[ScopeWrite]
			}
			if !allowed {
				description := fmt.Sprintf("Authorization required for scope %s.", taskScope)
				server.logger.Warn("mcp auth decision", "jsonrpc_method", method, "method_source", requestMethod.Source, "auth_present", true, "auth_valid", true, "decision", "challenge", "required_scope", taskScope, "reason", "insufficient_scope")
				writeBearerChallenge(w, server.ResourceMetadataURL(), taskScope, http.StatusForbidden, "insufficient_scope", description)
				return
			}
			server.logger.Info("mcp auth decision", "jsonrpc_method", method, "method_source", requestMethod.Source, "auth_present", true, "auth_valid", true, "client_id", identity.ClientID, "decision", "allow", "required_scope", taskScope)
			r = r.WithContext(context.WithValue(r.Context(), identityContextKey{}, identity))
			next.ServeHTTP(w, r)
			return
		}

		if !present {
			server.logger.Info("mcp auth decision", "jsonrpc_method", method, "method_source", requestMethod.Source, "auth_present", false, "decision", "challenge")
			writeBearerChallenge(w, server.ResourceMetadataURL(), "", http.StatusUnauthorized, "", "")
			return
		}
		identity, err := server.VerifyBearer(raw, "")
		if err != nil {
			server.logger.Warn("mcp auth decision", "jsonrpc_method", method, "method_source", requestMethod.Source, "auth_present", true, "auth_valid", false, "decision", "challenge", "reason", err.Error())
			writeBearerChallenge(w, server.ResourceMetadataURL(), "", http.StatusUnauthorized, "invalid_token", err.Error())
			return
		}
		server.logger.Info("mcp auth decision", "jsonrpc_method", method, "method_source", requestMethod.Source, "auth_present", true, "auth_valid", true, "client_id", identity.ClientID, "decision", "allow")
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityContextKey{}, identity)))
	})
}
func bearerToken(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func writeBearerChallenge(w http.ResponseWriter, metadataURL, scope string, status int, code, description string) {
	params := []string{fmt.Sprintf(`resource_metadata="%s"`, escapeChallenge(metadataURL))}
	if scope != "" {
		params = append(params, fmt.Sprintf(`scope="%s"`, escapeChallenge(scope)))
	}
	if code != "" {
		params = append(params, fmt.Sprintf(`error="%s"`, escapeChallenge(code)))
	}
	if description != "" {
		params = append(params, fmt.Sprintf(`error_description="%s"`, escapeChallenge(description)))
	}
	w.Header().Set("WWW-Authenticate", "Bearer "+strings.Join(params, ", "))
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, http.StatusText(status), status)
}

func escapeChallenge(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	return strings.ReplaceAll(value, "\"", "\\\"")
}

// InjectToolSecuritySchemes is a compatibility adapter for the current Go MCP
// SDK, whose Tool type does not yet expose the ChatGPT securitySchemes field.
// It mutates only JSON tools/list responses and leaves all other MCP traffic untouched.
func InjectToolSecuritySchemes(next http.Handler, resolve ScopeResolver, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestMethod, err := resolveMCPRequestMethod(r)
		if err != nil {
			logger.Warn("mcp security scheme injection skipped", "reason", "invalid_jsonrpc_request", "error", err)
			next.ServeHTTP(w, r)
			return
		}
		if requestMethod.Method != "tools/list" {
			next.ServeHTTP(w, r)
			return
		}
		capture := newCaptureWriter()
		next.ServeHTTP(capture, r)
		body := capture.body.Bytes()
		if capture.status >= 200 && capture.status < 300 && strings.Contains(capture.header.Get("Content-Type"), "application/json") {
			if mutated, err := injectSecuritySchemes(body, resolve); err == nil {
				body = mutated
				logger.Info("mcp security schemes injected", "jsonrpc_method", requestMethod.Method, "method_source", requestMethod.Source)
			} else {
				logger.Warn("mcp security scheme injection failed", "jsonrpc_method", requestMethod.Method, "method_source", requestMethod.Source, "error", err)
			}
		}
		for key, values := range capture.header {
			w.Header()[key] = append([]string(nil), values...)
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		w.WriteHeader(capture.status)
		_, _ = w.Write(body)
	})
}

func injectSecuritySchemes(body []byte, resolve ScopeResolver) ([]byte, error) {
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	result, ok := envelope["result"].(map[string]any)
	if !ok {
		return body, nil
	}
	items, ok := result["tools"].([]any)
	if !ok {
		return body, nil
	}
	for _, item := range items {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := tool["name"].(string)
		tool["securitySchemes"] = []map[string]any{{"type": "oauth2", "scopes": []string{resolve(name)}}}
	}
	return json.Marshal(envelope)
}

type captureWriter struct {
	header        http.Header
	body          bytes.Buffer
	status        int
	statusWritten bool
}

func newCaptureWriter() *captureWriter {
	return &captureWriter{header: make(http.Header), status: http.StatusOK}
}
func (w *captureWriter) Header() http.Header { return w.header }
func (w *captureWriter) WriteHeader(status int) {
	if w.statusWritten {
		return
	}
	w.status = status
	w.statusWritten = true
}
func (w *captureWriter) Write(data []byte) (int, error) {
	if !w.statusWritten {
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(data)
}

func taskMethodScope(method string) string {
	switch method {
	case "tasks/get":
		return ScopeRead
	case "tasks/update", "tasks/cancel":
		return ScopeWrite
	default:
		return ""
	}
}

// EnforceToolScopes runs after the MCP SDK has parsed the JSON-RPC request.
// It is the authoritative scope check; HTTP Mcp-Name headers are only an early
// rejection optimization and are never trusted as the sole authorization input.
func EnforceToolScopes(server *Server, resolve ScopeResolver) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			name := ""
			switch params := req.GetParams().(type) {
			case *mcp.CallToolParamsRaw:
				name = params.Name
			case *mcp.CallToolParams:
				name = params.Name
			default:
				return nil, fmt.Errorf("unexpected tools/call parameter type %T", req.GetParams())
			}
			required := resolve(name)
			identity, ok := IdentityFromContext(ctx)
			if ok {
				if _, granted := identity.Scopes[required]; granted {
					return next(ctx, method, req)
				}
			}
			code := "invalid_token"
			description := "Authentication required: no valid access token provided."
			if ok {
				code = "insufficient_scope"
				description = fmt.Sprintf("Authorization required for scope %s.", required)
			}
			challenge := fmt.Sprintf(`Bearer resource_metadata="%s", scope="%s", error="%s", error_description="%s"`,
				server.ResourceMetadataURL(), required, code, description)
			return &mcp.CallToolResult{
				Meta:    mcp.Meta{"mcp/www_authenticate": []string{challenge}},
				Content: []mcp.Content{&mcp.TextContent{Text: description}},
				IsError: true,
			}, nil
		}
	}
}

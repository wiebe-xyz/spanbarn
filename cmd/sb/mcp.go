package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
)

const mcpDefaultProtocol = "2025-06-18"

// mcpFetch performs one read-only GET against the SpanBarn API.
type mcpFetch func(endpoint string, params url.Values) (json.RawMessage, error)

type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// cmdMCP runs a read-only MCP server on stdio. It reuses the sb login, so
// "sb login" first. Every tool is a GET; nothing here can change SpanBarn.
func cmdMCP(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("usage: sb mcp (speaks MCP over stdio; takes no flags)")
	}
	client, err := newClient()
	if err != nil {
		return err
	}
	return serveMCP(os.Stdin, os.Stdout, clientFetcher(client))
}

// clientFetcher resolves the project slug carried in params to a project_id and
// queries through the authenticated client.
func clientFetcher(c *Client) mcpFetch {
	return func(endpoint string, params url.Values) (json.RawMessage, error) {
		slug := params.Get(mcpProjectParam)
		params.Del(mcpProjectParam)
		if len(params) == 0 {
			return c.get(endpoint)
		}
		c.project = resolveProject(slug, c.cfg)
		c.projectID = 0
		return c.query(endpoint, params)
	}
}

// serveMCP reads newline-delimited JSON-RPC messages from in and writes
// responses to out until in is closed.
func serveMCP(in io.Reader, out io.Writer, fetch mcpFetch) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			if err := enc.Encode(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}}); err != nil {
				return err
			}
			continue
		}
		if len(req.ID) == 0 {
			continue // notification: no response
		}
		if err := enc.Encode(handleRPC(req, fetch)); err != nil {
			return err
		}
	}
	return sc.Err()
}

func handleRPC(req rpcRequest, fetch mcpFetch) rpcResponse {
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = mcpInitialize(req.Params)
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": mcpToolList()}
	case "tools/call":
		resp.Result = mcpCall(req.Params, fetch)
	default:
		resp.Error = &rpcError{-32601, "method not found: " + req.Method}
	}
	return resp
}

func mcpInitialize(params json.RawMessage) map[string]any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)
	if p.ProtocolVersion == "" {
		p.ProtocolVersion = mcpDefaultProtocol
	}
	return map[string]any{
		"protocolVersion": p.ProtocolVersion,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "spanbarn", "version": Version},
	}
}

func mcpToolList() []map[string]any {
	list := make([]map[string]any, 0, len(mcpTools))
	for _, t := range mcpTools {
		list = append(list, map[string]any{"name": t.name, "description": t.desc, "inputSchema": t.schema()})
	}
	return list
}

// mcpCall runs one tool. Failures come back as tool results with isError set,
// so the model sees the message.
func mcpCall(params json.RawMessage, fetch mcpFetch) map[string]any {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return mcpToolResult("invalid tool call: "+err.Error(), true)
	}
	tool, ok := findMCPTool(p.Name)
	if !ok {
		return mcpToolResult("unknown tool: "+p.Name, true)
	}
	endpoint, query, err := tool.request(p.Arguments)
	if err != nil {
		return mcpToolResult(err.Error(), true)
	}
	data, err := fetch(endpoint, query)
	if err != nil {
		return mcpToolResult(err.Error(), true)
	}
	return mcpToolResult(string(data), false)
}

func mcpToolResult(text string, isError bool) map[string]any {
	res := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
	if isError {
		res["isError"] = true
	}
	return res
}

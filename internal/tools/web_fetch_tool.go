package tools

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxWebFetchBytes = 100_000
const webFetchTimeout = 20 * time.Second

type webFetchTool struct {
	client       *http.Client
	allowPrivate bool
}

// NewWebFetchTool creates a public HTTP(S) document reader.
func NewWebFetchTool() Definition {
	return &webFetchTool{client: newPublicHTTPClient()}
}

func (*webFetchTool) Name() string { return "WebFetch" }
func (*webFetchTool) Description() string {
	return "Fetch an HTTP(S) text document. Private and local addresses require full-access or YOLO mode."
}
func (*webFetchTool) InputSchema() JSONSchema {
	return objectSchema(map[string]any{
		"url": stringSchema("Public http or https URL to fetch."),
	}, "url")
}
func (t *webFetchTool) Execute(parent context.Context, arguments map[string]any, toolContext Context) (Result, error) {
	value, err := requireString(arguments, "url")
	if err != nil {
		return Result{}, fmt.Errorf("WebFetch tool requires a non-empty 'url' string.")
	}
	target, err := url.Parse(value)
	if err != nil {
		return Result{}, fmt.Errorf("invalid URL: %w", err)
	}
	allowPrivate := t.allowPrivate || toolContext.PermissionMode().HasFullAccess()
	if err := validateWebURL(parent, target, allowPrivate); err != nil {
		return Result{}, err
	}
	allowed, err := toolContext.RequestPermission(parent, ApprovalRequest{
		ToolName: "WebFetch", Kind: ApprovalNetwork, Scope: "web-fetch", Command: "GET " + target.String(),
		Justification: "读取公共网络上的文本文档",
	})
	if err != nil {
		return Result{}, err
	}
	if !allowed {
		return Result{Content: "User denied network access.", IsError: true}, nil
	}
	ctx, cancel := context.WithTimeout(parent, webFetchTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return Result{}, err
	}
	request.Header.Set("Accept", "text/html, text/plain, application/json, application/xml;q=0.9, */*;q=0.1")
	request.Header.Set("User-Agent", "Banka-Code/0.1 WebFetch")
	client := t.client
	if allowPrivate && !t.allowPrivate {
		client = newHTTPClient(true)
	}
	response, err := client.Do(request)
	if err != nil {
		return Result{}, err
	}
	defer response.Body.Close()
	contentType := response.Header.Get("Content-Type")
	if !isTextContentType(contentType) {
		return Result{Content: fmt.Sprintf("Unsupported response content type: %s", contentType), IsError: true}, nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxWebFetchBytes+1))
	if err != nil {
		return Result{}, err
	}
	truncated := len(body) > maxWebFetchBytes
	if truncated {
		body = body[:maxWebFetchBytes]
	}
	content := fmt.Sprintf("url: %s\nstatus: %s\ncontent_type: %s\n\n%s", response.Request.URL, response.Status, contentType, body)
	if truncated {
		content += "\n\n[truncated]"
	}
	return Result{Content: content, IsError: response.StatusCode < 200 || response.StatusCode >= 300}, nil
}

func newPublicHTTPClient() *http.Client {
	return newHTTPClient(false)
}

func newHTTPClient(allowPrivate bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, network string, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("host resolved to no addresses: %s", host)
		}
		for _, address := range addresses {
			if !allowPrivate && !isPublicIP(address.IP) {
				return nil, fmt.Errorf("WebFetch blocks non-public address for host %s", host)
			}
		}
		var lastErr error
		for _, address := range addresses {
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(address.IP.String(), port))
			if err == nil {
				return connection, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return validateWebURL(request.Context(), request.URL, allowPrivate)
		},
	}
}

func validateWebURL(ctx context.Context, target *url.URL, allowPrivate bool) error {
	if target.Scheme != "http" && target.Scheme != "https" {
		return errorsForURL("only http and https URLs are supported")
	}
	if target.Hostname() == "" || target.User != nil {
		return errorsForURL("URL must contain a host and no user credentials")
	}
	if allowPrivate {
		return nil
	}
	host := strings.ToLower(strings.TrimSuffix(target.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return errorsForURL("local hosts are blocked")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return err
	}
	for _, address := range addresses {
		if !isPublicIP(address.IP) {
			return errorsForURL("private and local network addresses are blocked")
		}
	}
	return nil
}

func isPublicIP(ip net.IP) bool {
	return ip != nil && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() && !ip.IsUnspecified() && !ip.IsMulticast()
}

func isTextContentType(contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	return contentType == "" || strings.HasPrefix(contentType, "text/") ||
		contentType == "application/json" || contentType == "application/xml" ||
		contentType == "application/javascript" || strings.HasSuffix(contentType, "+json") || strings.HasSuffix(contentType, "+xml")
}

func errorsForURL(message string) error {
	return fmt.Errorf("WebFetch rejected URL: %s", message)
}

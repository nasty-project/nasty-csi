// Package tnsapi provides a WebSocket client for the NASty storage API.
package tnsapi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/nasty-project/nasty-csi/pkg/metrics"
	"k8s.io/klog/v2"
)

// Static errors for client operations.
var (
	ErrAuthenticationRejected = errors.New("authentication failed: NASty rejected API token - verify the token is correct and not expired")
	ErrClientClosed           = errors.New("client is closed")
	ErrConnectionClosed       = errors.New("connection closed while waiting for response")
	// ErrDatasetNotFound is kept for compatibility with FindSubvolumeByCSIVolumeName.
	ErrDatasetNotFound = errors.New("dataset not found")
	// ErrPoolNotFound is returned when a requested pool is not found.
	ErrPoolNotFound = errors.New("pool not found")
	// ErrNFSShareDeletionFailed is kept for NFS share deletion error reporting.
	ErrNFSShareDeletionFailed = errors.New("NFS share deletion returned false (unsuccessful)")
)

// Client is a storage API client using JSON-RPC 2.0 over WebSocket.
//
//nolint:govet // fieldalignment: struct field order optimized for readability over memory layout
type Client struct {
	mu            sync.Mutex
	conn          *websocket.Conn
	pending       map[string]chan *Response
	closeCh       chan struct{}
	url           string
	apiKey        string
	connectedAt   time.Time // Track connection start time for metrics
	retryInterval time.Duration
	reqID         uint64
	maxRetries    int
	closed        bool
	reconnecting  bool
	skipTLSVerify bool // Skip TLS certificate verification
}

// Request represents a storage API WebSocket request (JSON-RPC 2.0 format).
type Request struct {
	ID      string        `json:"id"`
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params,omitempty"`
}

// Response represents a storage API WebSocket response.
type Response struct {
	Error  *Error          `json:"error,omitempty"`
	ID     string          `json:"id"`
	Msg    string          `json:"msg,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

// Error represents a storage API error.
type Error struct {
	Data      *ErrorData `json:"data,omitempty"`
	ErrorName string     `json:"errname"`
	Reason    string     `json:"reason"`
	Type      string     `json:"type"`
	Message   string     `json:"message"`
	ErrorCode int        `json:"error"`
	Code      int        `json:"code"`
}

// ErrorData represents the structured error data from NASty API responses.
//
//nolint:govet // fieldalignment: keeping fields in logical order for readability
type ErrorData struct {
	Error     int         `json:"error"`
	ErrorName string      `json:"errname"`
	Reason    string      `json:"reason"`
	Trace     *ErrorTrace `json:"trace,omitempty"`
	Extra     interface{} `json:"extra,omitempty"`
}

// ErrorTrace represents stack trace information from NASty API errors.
type ErrorTrace struct {
	Class     string      `json:"class"`
	Frames    interface{} `json:"-"` // Stack frames (omitted from JSON)
	Formatted string      `json:"-"` // Formatted trace (omitted from JSON)
	Repr      string      `json:"repr"`
}

func (e *Error) Error() string {
	// Try storage API error format first (using top-level Reason field)
	if e.Reason != "" {
		return fmt.Sprintf("Storage API error [%s]: %s", e.ErrorName, e.Reason)
	}
	// Fallback to JSON-RPC 2.0 format with structured error data
	if e.Data != nil {
		// Try to format Data as JSON for better error messages
		if dataBytes, err := json.Marshal(e.Data); err == nil {
			return fmt.Sprintf("Storage API error %d: %s (data: %s)", e.Code, e.Message, string(dataBytes))
		}
		return fmt.Sprintf("Storage API error %d: %s (data: %v)", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("Storage API error %d: %s", e.Code, e.Message)
}

// isPermanentConnectionError checks if a connection error is permanent (e.g. invalid URL/port).
// Permanent errors should not be retried.
func isPermanentConnectionError(err error) bool {
	if err == nil {
		return false
	}
	errMsg := err.Error()
	return strings.Contains(errMsg, "invalid port") ||
		strings.Contains(errMsg, "no such host") ||
		strings.Contains(errMsg, "invalid URL")
}

// isAuthenticationError checks if an error is a permanent authentication failure.
func isAuthenticationError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrAuthenticationRejected) {
		return true
	}
	errMsg := err.Error()
	return strings.Contains(errMsg, "401") ||
		strings.Contains(errMsg, "invalid API key") ||
		strings.Contains(errMsg, "rejected API key") ||
		(strings.Contains(errMsg, "authentication failed") && strings.Contains(errMsg, "500"))
}

// NewClient creates a new storage API client.
// skipTLSVerify should be set to true only for self-signed certificates.
func NewClient(url, apiKey string, skipTLSVerify bool) (*Client, error) {
	klog.V(4).Infof("Creating new storage API client for %s (skipTLSVerify=%v)", url, skipTLSVerify)

	apiKey = strings.TrimSpace(apiKey)
	klog.V(5).Infof("API key length after trim: %d characters", len(apiKey))

	c := &Client{
		url:           url,
		apiKey:        apiKey,
		pending:       make(map[string]chan *Response),
		closeCh:       make(chan struct{}),
		maxRetries:    5,
		retryInterval: 5 * time.Second,
		skipTLSVerify: skipTLSVerify,
	}

	maxAttempts := 5
	retryDelays := []time.Duration{0, 5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second}

	var lastConnErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			klog.Warningf("Connection attempt %d/%d to NASty failed: %v", attempt-1, maxAttempts, lastConnErr)
			delay := retryDelays[attempt-1]
			klog.Infof("Retrying connection in %v...", delay)
			time.Sleep(delay)

			c = &Client{
				url:           url,
				apiKey:        apiKey,
				pending:       make(map[string]chan *Response),
				closeCh:       make(chan struct{}),
				maxRetries:    5,
				retryInterval: 5 * time.Second,
				skipTLSVerify: skipTLSVerify,
			}
		}

		klog.V(4).Infof("Attempting to connect to NASty (attempt %d/%d)", attempt, maxAttempts)

		if err := c.connect(); err != nil {
			lastConnErr = err
			if isPermanentConnectionError(err) {
				return nil, fmt.Errorf("failed to connect: %w", err)
			}
			if attempt == maxAttempts {
				return nil, fmt.Errorf("failed to connect after %d attempts: %w", maxAttempts, err)
			}
			continue
		}

		if err := c.authenticate(); err != nil {
			c.Close()
			lastConnErr = err

			if errors.Is(err, ErrAuthenticationRejected) || isAuthenticationError(err) {
				klog.Errorf("Authentication failed permanently: %v", err)
				return nil, fmt.Errorf("authentication failed: %w", err)
			}

			if attempt == maxAttempts {
				return nil, fmt.Errorf("failed to authenticate after %d attempts: %w", maxAttempts, err)
			}
			continue
		}

		go c.readLoop()
		go c.pingLoop()

		if attempt > 1 {
			klog.Infof("Successfully connected to NASty on attempt %d/%d", attempt, maxAttempts)
		} else {
			klog.V(4).Infof("Successfully connected to NASty")
		}
		return c, nil
	}

	return nil, fmt.Errorf("failed to initialize client after %d attempts: %w", maxAttempts, lastConnErr)
}

// connect establishes WebSocket connection.
func (c *Client) connect() error {
	klog.V(4).Infof("Connecting to storage WebSocket at %s", c.url)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	httpClient := &http.Client{}

	if strings.HasPrefix(c.url, "wss://") {
		var tlsConfig *tls.Config
		if c.skipTLSVerify {
			klog.V(4).Info("TLS certificate verification disabled (skipTLSVerify=true)")
			//nolint:gosec // G402: TLS InsecureSkipVerify set true - intentional for self-signed certs
			tlsConfig = &tls.Config{
				InsecureSkipVerify: true,
				MinVersion:         tls.VersionTLS12,
			}
		} else {
			tlsConfig = &tls.Config{
				MinVersion: tls.VersionTLS12,
			}
		}
		httpClient.Transport = &http.Transport{
			TLSClientConfig: tlsConfig,
		}
	}

	conn, resp, err := websocket.Dial(ctx, c.url, &websocket.DialOptions{
		HTTPClient: httpClient,
	})
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("failed to dial: %w", err)
	}

	conn.SetReadLimit(10 * 1024 * 1024)

	c.conn = conn
	c.connectedAt = time.Now()

	metrics.SetWSConnectionStatus(true)

	return nil
}

// authenticate sends the API token and waits for NASty's confirmation.
func (c *Client) authenticate() error {
	return c.doAuth()
}

// authenticateDirect is an alias for authenticate used during reconnection.
func (c *Client) authenticateDirect() error {
	return c.doAuth()
}

// doAuth sends {"token": "..."} and reads NASty's {"authenticated": true, ...} response.
func (c *Client) doAuth() error {
	klog.V(4).Info("Authenticating with NASty using API token")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	authMsg := map[string]string{"token": c.apiKey}
	if err := wsjson.Write(ctx, c.conn, authMsg); err != nil {
		return fmt.Errorf("failed to send auth token: %w", err)
	}

	_, rawMsg, err := c.conn.Read(ctx)
	if err != nil {
		return fmt.Errorf("failed to read auth response: %w", err)
	}

	klog.V(5).Infof("Auth response: %s", string(rawMsg))

	var result map[string]interface{}
	if err := json.Unmarshal(rawMsg, &result); err != nil {
		return fmt.Errorf("failed to parse auth response: %w", err)
	}

	if errMsg, ok := result["error"].(string); ok {
		klog.Errorf("NASty rejected API token: %s", errMsg)
		return ErrAuthenticationRejected
	}

	if auth, _ := result["authenticated"].(bool); !auth {
		klog.Errorf("NASty auth response missing 'authenticated' field")
		return ErrAuthenticationRejected
	}

	klog.V(4).Infof("Authenticated with NASty as '%v' (role: %v)", result["username"], result["role"])
	return nil
}

// isConnectionError checks if the error is a connection-related error that should trigger a retry.
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrConnectionClosed) {
		return true
	}
	errStr := err.Error()
	return strings.Contains(errStr, "use of closed network connection") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "i/o timeout")
}

// Call makes a JSON-RPC 2.0 call with automatic retry on connection failures.
func (c *Client) Call(ctx context.Context, method string, params []interface{}, result interface{}) error {
	timer := metrics.NewWSMessageTimer(method)
	defer timer.Observe()

	const maxRetries = 3
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := c.callOnce(ctx, method, params, result)
		if err == nil {
			return nil
		}

		lastErr = err

		if !isConnectionError(err) {
			return err
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return ErrClientClosed
		}

		if attempt < maxRetries {
			backoff := time.Duration(1<<(attempt-1)) * time.Second
			klog.V(4).Infof("Request failed with connection error (attempt %d/%d): %v, retrying in %v...",
				attempt, maxRetries, err, backoff)

			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			case <-c.closeCh:
				return ErrClientClosed
			}
		}
	}

	return fmt.Errorf("request failed after %d attempts: %w", maxRetries, lastErr)
}

// callOnce makes a single JSON-RPC 2.0 call attempt.
func (c *Client) callOnce(ctx context.Context, method string, params []interface{}, result interface{}) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClientClosed
	}

	id := strconv.FormatUint(atomic.AddUint64(&c.reqID, 1), 10)

	req := &Request{
		ID:      id,
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}

	respCh := make(chan *Response, 1)
	c.pending[id] = respCh

	klog.V(5).Infof("Sending request: method=%s, id=%s", method, id)
	writeCtx, writeCancel := context.WithTimeout(ctx, 10*time.Second)
	err := wsjson.Write(writeCtx, c.conn, req)
	writeCancel()
	if err != nil {
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("failed to send request: %w", err)
	}
	metrics.RecordWSMessage("sent")
	c.mu.Unlock()

	select {
	case resp, ok := <-respCh:
		if !ok {
			return ErrConnectionClosed
		}
		metrics.RecordWSMessage("received")
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil && resp.Result != nil {
			if err := json.Unmarshal(resp.Result, result); err != nil {
				return fmt.Errorf("failed to unmarshal result: %w", err)
			}
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	case <-c.closeCh:
		return ErrClientClosed
	}
}

// readLoop reads responses from WebSocket.
func (c *Client) readLoop() {
	defer c.cleanupReadLoop()

	for {
		_, rawMsg, err := c.conn.Read(context.Background())

		if err != nil {
			if c.handleReadError(err) {
				continue
			}
			return
		}

		c.processResponse(rawMsg)
	}
}

// cleanupReadLoop performs cleanup when readLoop exits.
func (c *Client) cleanupReadLoop() {
	c.mu.Lock()
	c.closed = true
	for _, ch := range c.pending {
		close(ch)
	}
	c.pending = make(map[string]chan *Response)
	c.mu.Unlock()
	close(c.closeCh)
}

// handleReadError handles WebSocket read errors with reconnection logic.
func (c *Client) handleReadError(err error) bool {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return false
	}
	c.mu.Unlock()

	closeStatus := websocket.CloseStatus(err)
	if closeStatus != websocket.StatusNormalClosure && closeStatus != websocket.StatusGoingAway {
		klog.Errorf("WebSocket read error: %v", err)
	}

	if c.reconnect() {
		klog.Info("Successfully reconnected to storage WebSocket")
		return true
	}

	return c.reinitializeConnection()
}

// reinitializeConnection performs full connection reinitialization after reconnect failures.
func (c *Client) reinitializeConnection() bool {
	klog.Warning("Failed to reconnect after 5 attempts, will reinitialize connection in 30 seconds...")
	time.Sleep(30 * time.Second)

	klog.Info("Reinitializing WebSocket connection from scratch...")
	if err := c.connect(); err != nil {
		klog.Errorf("Connection reinitialization failed: %v, will retry", err)
		return true
	}

	if err := c.authenticateDirect(); err != nil {
		klog.Errorf("Re-authentication after reinitialization failed: %v, will retry", err)
		return true
	}

	klog.Info("Successfully reinitialized WebSocket connection")
	return true
}

// processResponse unmarshals and dispatches a response to the waiting caller.
func (c *Client) processResponse(rawMsg []byte) {
	klog.V(5).Infof("Received raw response: %s", string(rawMsg))

	var resp Response
	if err := json.Unmarshal(rawMsg, &resp); err != nil {
		klog.Errorf("Failed to unmarshal response: %v", err)
		return
	}

	klog.V(5).Infof("Parsed response: %+v", resp)

	c.mu.Lock()
	if ch, ok := c.pending[resp.ID]; ok {
		delete(c.pending, resp.ID)
		ch <- &resp
		close(ch)
	}
	c.mu.Unlock()
}

// reconnect attempts to reconnect to the WebSocket and re-authenticate.
func (c *Client) reconnect() bool {
	c.mu.Lock()
	if c.reconnecting {
		c.mu.Unlock()
		return false
	}
	c.reconnecting = true
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.reconnecting = false
		c.mu.Unlock()
	}()

	klog.Warning("WebSocket connection lost, attempting to reconnect...")

	metrics.SetWSConnectionStatus(false)

	for attempt := 1; attempt <= c.maxRetries; attempt++ {
		metrics.RecordWSReconnection()
		shift := attempt - 1
		if shift < 0 {
			shift = 0
		}
		backoff := time.Duration(1<<shift) * c.retryInterval
		if backoff > 60*time.Second {
			backoff = 60 * time.Second
		}

		klog.Infof("Reconnection attempt %d/%d (waiting %v)...", attempt, c.maxRetries, backoff)
		select {
		case <-time.After(backoff):
		case <-c.closeCh:
			klog.Info("Reconnection canceled - client is closing")
			return false
		}

		c.mu.Lock()
		if c.conn != nil {
			//nolint:errcheck,gosec // G104: Intentionally ignoring close error during reconnection
			c.conn.Close(websocket.StatusGoingAway, "reconnecting")
		}
		for _, ch := range c.pending {
			close(ch)
		}
		c.pending = make(map[string]chan *Response)
		c.mu.Unlock()

		if err := c.connect(); err != nil {
			klog.Errorf("Reconnection attempt %d failed: %v", attempt, err)
			continue
		}

		if err := c.authenticateDirect(); err != nil {
			klog.Errorf("Re-authentication attempt %d failed: %v", attempt, err)
			continue
		}

		klog.Infof("Successfully reconnected on attempt %d", attempt)
		return true
	}

	return false
}

// pingLoop sends periodic pings to keep the connection alive.
func (c *Client) pingLoop() {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.mu.Lock()
			if c.closed || c.conn == nil || c.reconnecting {
				c.mu.Unlock()
				if c.reconnecting {
					continue
				}
				return
			}

			if !c.connectedAt.IsZero() {
				metrics.SetWSConnectionDuration(time.Since(c.connectedAt))
			}

			conn := c.conn
			c.mu.Unlock()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := conn.Ping(ctx)
			cancel()

			if err != nil {
				klog.Warningf("Failed to send ping: %v", err)
				continue
			}

			klog.V(6).Info("Sent WebSocket ping")

		case <-c.closeCh:
			return
		}
	}
}

// Close closes the client connection.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return
	}

	klog.V(4).Info("Closing storage API client")
	c.closed = true

	if c.conn != nil {
		//nolint:errcheck,gosec // G104: Intentionally ignoring close error during shutdown
		c.conn.Close(websocket.StatusNormalClosure, "client closing")
	}
}

// ── NASty API method implementations ──────────────────────────────────────────
//
// All methods implement ClientInterface using the NASty JSON-RPC 2.0 API.

// QueryPool retrieves information about a specific pool.
func (c *Client) QueryPool(ctx context.Context, poolName string) (*Pool, error) {
	klog.V(4).Infof("Querying pool: %s", poolName)

	var result Pool
	if err := c.Call(ctx, "pool.get", []interface{}{
		map[string]interface{}{"name": poolName},
	}, &result); err != nil {
		return nil, fmt.Errorf("failed to query pool %s: %w", poolName, err)
	}

	klog.V(4).Infof("Queried pool %s: total=%d used=%d available=%d",
		result.Name, result.TotalBytes, result.UsedBytes, result.AvailableBytes)
	return &result, nil
}

// CreateSubvolume creates a new subvolume (filesystem or block device).
func (c *Client) CreateSubvolume(ctx context.Context, params SubvolumeCreateParams) (*Subvolume, error) {
	klog.V(4).Infof("Creating subvolume %s/%s (type=%s)", params.Pool, params.Name, params.SubvolumeType)

	var result Subvolume
	if err := c.Call(ctx, "subvolume.create", []interface{}{
		map[string]interface{}{
			"pool":           params.Pool,
			"name":           params.Name,
			"subvolume_type": params.SubvolumeType,
			"volsize_bytes":  params.VolsizeBytes,
			"compression":    params.Compression,
			"comments":       params.Comments,
		},
	}, &result); err != nil {
		return nil, fmt.Errorf("failed to create subvolume %s/%s: %w", params.Pool, params.Name, err)
	}

	klog.V(4).Infof("Created subvolume %s/%s at %s", params.Pool, params.Name, result.Path)
	return &result, nil
}

// DeleteSubvolume deletes a subvolume.
func (c *Client) DeleteSubvolume(ctx context.Context, pool, name string) error {
	klog.V(4).Infof("Deleting subvolume %s/%s", pool, name)

	if err := c.Call(ctx, "subvolume.delete", []interface{}{
		map[string]interface{}{"pool": pool, "name": name},
	}, nil); err != nil {
		return fmt.Errorf("failed to delete subvolume %s/%s: %w", pool, name, err)
	}

	klog.V(4).Infof("Deleted subvolume %s/%s", pool, name)
	return nil
}

// GetSubvolume retrieves a subvolume by pool and name.
func (c *Client) GetSubvolume(ctx context.Context, pool, name string) (*Subvolume, error) {
	klog.V(4).Infof("Getting subvolume %s/%s", pool, name)

	var result Subvolume
	if err := c.Call(ctx, "subvolume.get", []interface{}{
		map[string]interface{}{"pool": pool, "name": name},
	}, &result); err != nil {
		return nil, fmt.Errorf("failed to get subvolume %s/%s: %w", pool, name, err)
	}

	return &result, nil
}

// ListAllSubvolumes lists all subvolumes in a pool.
func (c *Client) ListAllSubvolumes(ctx context.Context, pool string) ([]Subvolume, error) {
	klog.V(4).Infof("Listing subvolumes in pool %s", pool)

	var result []Subvolume
	if err := c.Call(ctx, "subvolume.list_all", []interface{}{
		map[string]interface{}{"pool": pool},
	}, &result); err != nil {
		return nil, fmt.Errorf("failed to list subvolumes in pool %s: %w", pool, err)
	}

	klog.V(4).Infof("Found %d subvolumes in pool %s", len(result), pool)
	return result, nil
}

// SetSubvolumeProperties sets xattr properties on a subvolume.
func (c *Client) SetSubvolumeProperties(ctx context.Context, pool, name string, props map[string]string) (*Subvolume, error) {
	klog.V(4).Infof("Setting %d properties on subvolume %s/%s", len(props), pool, name)

	var result Subvolume
	if err := c.Call(ctx, "subvolume.set_properties", []interface{}{
		map[string]interface{}{
			"pool":       pool,
			"name":       name,
			"properties": props,
		},
	}, &result); err != nil {
		return nil, fmt.Errorf("failed to set properties on subvolume %s/%s: %w", pool, name, err)
	}

	klog.V(4).Infof("Set properties on subvolume %s/%s", pool, name)
	return &result, nil
}

// RemoveSubvolumeProperties removes xattr properties from a subvolume.
func (c *Client) RemoveSubvolumeProperties(ctx context.Context, pool, name string, keys []string) (*Subvolume, error) {
	klog.V(4).Infof("Removing %d properties from subvolume %s/%s", len(keys), pool, name)

	var result Subvolume
	if err := c.Call(ctx, "subvolume.remove_properties", []interface{}{
		map[string]interface{}{
			"pool": pool,
			"name": name,
			"keys": keys,
		},
	}, &result); err != nil {
		return nil, fmt.Errorf("failed to remove properties from subvolume %s/%s: %w", pool, name, err)
	}

	return &result, nil
}

// FindSubvolumesByProperty finds subvolumes by an xattr property key/value pair.
func (c *Client) FindSubvolumesByProperty(ctx context.Context, key, value, pool string) ([]Subvolume, error) {
	klog.V(4).Infof("Finding subvolumes with %s=%s in pool %s", key, value, pool)

	var result []Subvolume
	if err := c.Call(ctx, "subvolume.find_by_property", []interface{}{
		map[string]interface{}{
			"key":   key,
			"value": value,
			"pool":  pool,
		},
	}, &result); err != nil {
		return nil, fmt.Errorf("failed to find subvolumes by property %s=%s: %w", key, value, err)
	}

	klog.V(4).Infof("Found %d subvolumes with %s=%s", len(result), key, value)
	return result, nil
}

// FindManagedSubvolumes finds all subvolumes managed by nasty-csi.
func (c *Client) FindManagedSubvolumes(ctx context.Context, pool string) ([]Subvolume, error) {
	return c.FindSubvolumesByProperty(ctx, PropertyManagedBy, ManagedByValue, pool)
}

// FindSubvolumeByCSIVolumeName finds a subvolume by its CSI volume name xattr.
// Returns ErrDatasetNotFound if no matching subvolume is found.
func (c *Client) FindSubvolumeByCSIVolumeName(ctx context.Context, pool, volumeName string) (*Subvolume, error) {
	klog.V(4).Infof("Finding subvolume by CSI volume name %s in pool %s", volumeName, pool)

	subvolumes, err := c.FindSubvolumesByProperty(ctx, PropertyCSIVolumeName, volumeName, pool)
	if err != nil {
		return nil, fmt.Errorf("failed to find subvolume by CSI volume name %s: %w", volumeName, err)
	}

	if len(subvolumes) == 0 {
		klog.V(4).Infof("No subvolume found with CSI volume name %s", volumeName)
		return nil, fmt.Errorf("%w: CSI volume name %s", ErrDatasetNotFound, volumeName)
	}

	if len(subvolumes) > 1 {
		klog.Warningf("Found %d subvolumes with CSI volume name %s, using first", len(subvolumes), volumeName)
	}

	klog.V(4).Infof("Found subvolume %s/%s for CSI volume name %s", subvolumes[0].Pool, subvolumes[0].Name, volumeName)
	return &subvolumes[0], nil
}

// CreateSnapshot creates a new snapshot of a subvolume.
func (c *Client) CreateSnapshot(ctx context.Context, params SnapshotCreateParams) (*Snapshot, error) {
	klog.V(4).Infof("Creating snapshot %s on subvolume %s/%s", params.Name, params.Pool, params.Subvolume)

	var result Snapshot
	if err := c.Call(ctx, "snapshot.create", []interface{}{
		map[string]interface{}{
			"pool":      params.Pool,
			"subvolume": params.Subvolume,
			"name":      params.Name,
			"read_only": params.ReadOnly,
		},
	}, &result); err != nil {
		return nil, fmt.Errorf("failed to create snapshot %s/%s@%s: %w", params.Pool, params.Subvolume, params.Name, err)
	}

	klog.V(4).Infof("Created snapshot %s/%s@%s", params.Pool, params.Subvolume, params.Name)
	return &result, nil
}

// DeleteSnapshot deletes a snapshot.
func (c *Client) DeleteSnapshot(ctx context.Context, pool, subvolume, name string) error {
	klog.V(4).Infof("Deleting snapshot %s/%s@%s", pool, subvolume, name)

	if err := c.Call(ctx, "snapshot.delete", []interface{}{
		map[string]interface{}{
			"pool":      pool,
			"subvolume": subvolume,
			"name":      name,
		},
	}, nil); err != nil {
		return fmt.Errorf("failed to delete snapshot %s/%s@%s: %w", pool, subvolume, name, err)
	}

	klog.V(4).Infof("Deleted snapshot %s/%s@%s", pool, subvolume, name)
	return nil
}

// ListSnapshots lists all snapshots in a pool.
func (c *Client) ListSnapshots(ctx context.Context, pool string) ([]Snapshot, error) {
	klog.V(4).Infof("Listing snapshots in pool %s", pool)

	var result []Snapshot
	if err := c.Call(ctx, "snapshot.list", []interface{}{
		map[string]interface{}{"pool": pool},
	}, &result); err != nil {
		return nil, fmt.Errorf("failed to list snapshots in pool %s: %w", pool, err)
	}

	klog.V(4).Infof("Found %d snapshots in pool %s", len(result), pool)
	return result, nil
}

// CreateNFSShare creates a new NFS share.
func (c *Client) CreateNFSShare(ctx context.Context, params NFSShareCreateParams) (*NFSShare, error) {
	klog.V(4).Infof("Creating NFS share for path: %s", params.Path)

	var result NFSShare
	if err := c.Call(ctx, "share.nfs.create", []interface{}{
		map[string]interface{}{
			"path":    params.Path,
			"comment": params.Comment,
			"clients": params.Clients,
			"enabled": params.Enabled,
		},
	}, &result); err != nil {
		return nil, fmt.Errorf("failed to create NFS share for %s: %w", params.Path, err)
	}

	klog.V(4).Infof("Created NFS share %s for path %s", result.ID, result.Path)
	return &result, nil
}

// DeleteNFSShare deletes an NFS share by UUID.
func (c *Client) DeleteNFSShare(ctx context.Context, id string) error {
	klog.V(4).Infof("Deleting NFS share %s", id)

	if err := c.Call(ctx, "share.nfs.delete", []interface{}{
		map[string]interface{}{"id": id},
	}, nil); err != nil {
		return fmt.Errorf("failed to delete NFS share %s: %w", id, err)
	}

	klog.V(4).Infof("Deleted NFS share %s", id)
	return nil
}

// ListNFSShares lists all NFS shares.
func (c *Client) ListNFSShares(ctx context.Context) ([]NFSShare, error) {
	klog.V(4).Info("Listing NFS shares")

	var result []NFSShare
	if err := c.Call(ctx, "share.nfs.list", []interface{}{
		map[string]interface{}{},
	}, &result); err != nil {
		return nil, fmt.Errorf("failed to list NFS shares: %w", err)
	}

	klog.V(4).Infof("Found %d NFS shares", len(result))
	return result, nil
}

// GetNFSShare retrieves a single NFS share by UUID.
func (c *Client) GetNFSShare(ctx context.Context, id string) (*NFSShare, error) {
	klog.V(4).Infof("Getting NFS share %s", id)

	var result NFSShare
	if err := c.Call(ctx, "share.nfs.get", []interface{}{
		map[string]interface{}{"id": id},
	}, &result); err != nil {
		return nil, fmt.Errorf("failed to get NFS share %s: %w", id, err)
	}

	return &result, nil
}

// CreateSMBShare creates a new SMB share.
func (c *Client) CreateSMBShare(ctx context.Context, params SMBShareCreateParams) (*SMBShare, error) {
	klog.V(4).Infof("Creating SMB share %q for path: %s", params.Name, params.Path)

	var result SMBShare
	if err := c.Call(ctx, "share.smb.create", []interface{}{
		map[string]interface{}{
			"name":    params.Name,
			"path":    params.Path,
			"comment": params.Comment,
		},
	}, &result); err != nil {
		return nil, fmt.Errorf("failed to create SMB share %q: %w", params.Name, err)
	}

	klog.V(4).Infof("Created SMB share %s (id=%s)", result.Name, result.ID)
	return &result, nil
}

// DeleteSMBShare deletes an SMB share by UUID.
func (c *Client) DeleteSMBShare(ctx context.Context, id string) error {
	klog.V(4).Infof("Deleting SMB share %s", id)

	if err := c.Call(ctx, "share.smb.delete", []interface{}{
		map[string]interface{}{"id": id},
	}, nil); err != nil {
		return fmt.Errorf("failed to delete SMB share %s: %w", id, err)
	}

	klog.V(4).Infof("Deleted SMB share %s", id)
	return nil
}

// ListSMBShares lists all SMB shares.
func (c *Client) ListSMBShares(ctx context.Context) ([]SMBShare, error) {
	klog.V(4).Info("Listing SMB shares")

	var result []SMBShare
	if err := c.Call(ctx, "share.smb.list", []interface{}{
		map[string]interface{}{},
	}, &result); err != nil {
		return nil, fmt.Errorf("failed to list SMB shares: %w", err)
	}

	klog.V(4).Infof("Found %d SMB shares", len(result))
	return result, nil
}

// GetSMBShare retrieves a single SMB share by UUID.
func (c *Client) GetSMBShare(ctx context.Context, id string) (*SMBShare, error) {
	klog.V(4).Infof("Getting SMB share %s", id)

	var result SMBShare
	if err := c.Call(ctx, "share.smb.get", []interface{}{
		map[string]interface{}{"id": id},
	}, &result); err != nil {
		return nil, fmt.Errorf("failed to get SMB share %s: %w", id, err)
	}

	return &result, nil
}

// CreateISCSITarget creates a new iSCSI target (empty, no LUNs).
func (c *Client) CreateISCSITarget(ctx context.Context, params ISCSITargetCreateParams) (*ISCSITarget, error) {
	klog.V(4).Infof("Creating iSCSI target %q", params.Name)

	var result ISCSITarget
	if err := c.Call(ctx, "share.iscsi.create", []interface{}{
		map[string]interface{}{
			"name":    params.Name,
			"alias":   nil,
			"portals": []map[string]interface{}{{"ip": "0.0.0.0", "port": 3260}},
		},
	}, &result); err != nil {
		return nil, fmt.Errorf("failed to create iSCSI target %q: %w", params.Name, err)
	}

	klog.V(4).Infof("Created iSCSI target %s (id=%s, iqn=%s)", params.Name, result.ID, result.IQN)
	return &result, nil
}

// AddISCSILun adds a LUN (backstore) to an existing iSCSI target.
func (c *Client) AddISCSILun(ctx context.Context, targetID, backstorePath string) (*ISCSITarget, error) {
	klog.V(4).Infof("Adding LUN %s to iSCSI target %s", backstorePath, targetID)

	var result ISCSITarget
	if err := c.Call(ctx, "share.iscsi.add_lun", []interface{}{
		map[string]interface{}{
			"id":             targetID,
			"backstore_path": backstorePath,
		},
	}, &result); err != nil {
		return nil, fmt.Errorf("failed to add LUN %s to iSCSI target %s: %w", backstorePath, targetID, err)
	}

	klog.V(4).Infof("Added LUN to iSCSI target %s", targetID)
	return &result, nil
}

// AddISCSIACL adds an initiator ACL to an iSCSI target.
func (c *Client) AddISCSIACL(ctx context.Context, targetID, initiatorIQN string) (*ISCSITarget, error) {
	klog.V(4).Infof("Adding ACL %s to iSCSI target %s", initiatorIQN, targetID)

	var result ISCSITarget
	if err := c.Call(ctx, "share.iscsi.add_acl", []interface{}{
		map[string]interface{}{
			"id":            targetID,
			"initiator_iqn": initiatorIQN,
		},
	}, &result); err != nil {
		return nil, fmt.Errorf("failed to add ACL %s to iSCSI target %s: %w", initiatorIQN, targetID, err)
	}

	klog.V(4).Infof("Added ACL to iSCSI target %s", targetID)
	return &result, nil
}

// DeleteISCSITarget deletes an iSCSI target by UUID.
func (c *Client) DeleteISCSITarget(ctx context.Context, id string) error {
	klog.V(4).Infof("Deleting iSCSI target %s", id)

	if err := c.Call(ctx, "share.iscsi.delete", []interface{}{
		map[string]interface{}{"id": id},
	}, nil); err != nil {
		return fmt.Errorf("failed to delete iSCSI target %s: %w", id, err)
	}

	klog.V(4).Infof("Deleted iSCSI target %s", id)
	return nil
}

// ListISCSITargets lists all iSCSI targets.
func (c *Client) ListISCSITargets(ctx context.Context) ([]ISCSITarget, error) {
	klog.V(4).Info("Listing iSCSI targets")

	var result []ISCSITarget
	if err := c.Call(ctx, "share.iscsi.list", []interface{}{
		map[string]interface{}{},
	}, &result); err != nil {
		return nil, fmt.Errorf("failed to list iSCSI targets: %w", err)
	}

	klog.V(4).Infof("Found %d iSCSI targets", len(result))
	return result, nil
}

// GetISCSITargetByIQN finds an iSCSI target by IQN.
// Returns nil, nil if not found.
func (c *Client) GetISCSITargetByIQN(ctx context.Context, iqn string) (*ISCSITarget, error) {
	klog.V(4).Infof("Looking up iSCSI target by IQN: %s", iqn)

	targets, err := c.ListISCSITargets(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list iSCSI targets for IQN lookup: %w", err)
	}

	for i := range targets {
		if targets[i].IQN == iqn {
			klog.V(4).Infof("Found iSCSI target %s for IQN %s", targets[i].ID, iqn)
			return &targets[i], nil
		}
	}

	klog.V(4).Infof("No iSCSI target found with IQN %s", iqn)
	return nil, nil //nolint:nilnil // nil, nil indicates "not found"
}

// CreateNVMeOFSubsystem creates a new NVMe-oF subsystem using the quick-create API.
func (c *Client) CreateNVMeOFSubsystem(ctx context.Context, params NVMeOFCreateParams) (*NVMeOFSubsystem, error) {
	klog.V(4).Infof("Creating NVMe-oF subsystem %q (device=%s)", params.Name, params.DevicePath)

	var result NVMeOFSubsystem
	if err := c.Call(ctx, "share.nvmeof.create_quick", []interface{}{
		map[string]interface{}{
			"name":        params.Name,
			"device_path": params.DevicePath,
			"addr":        params.Addr,
			"port":        params.Port,
			"hosts":       params.Hosts,
		},
	}, &result); err != nil {
		return nil, fmt.Errorf("failed to create NVMe-oF subsystem %q: %w", params.Name, err)
	}

	klog.V(4).Infof("Created NVMe-oF subsystem %s (id=%s, nqn=%s)", params.Name, result.ID, result.NQN)
	return &result, nil
}

// DeleteNVMeOFSubsystem deletes an NVMe-oF subsystem by UUID.
func (c *Client) DeleteNVMeOFSubsystem(ctx context.Context, id string) error {
	klog.V(4).Infof("Deleting NVMe-oF subsystem %s", id)

	if err := c.Call(ctx, "share.nvmeof.delete", []interface{}{
		map[string]interface{}{"id": id},
	}, nil); err != nil {
		return fmt.Errorf("failed to delete NVMe-oF subsystem %s: %w", id, err)
	}

	klog.V(4).Infof("Deleted NVMe-oF subsystem %s", id)
	return nil
}

// ListNVMeOFSubsystems lists all NVMe-oF subsystems.
func (c *Client) ListNVMeOFSubsystems(ctx context.Context) ([]NVMeOFSubsystem, error) {
	klog.V(4).Info("Listing NVMe-oF subsystems")

	var result []NVMeOFSubsystem
	if err := c.Call(ctx, "share.nvmeof.list", []interface{}{
		map[string]interface{}{},
	}, &result); err != nil {
		return nil, fmt.Errorf("failed to list NVMe-oF subsystems: %w", err)
	}

	klog.V(4).Infof("Found %d NVMe-oF subsystems", len(result))
	return result, nil
}

// GetNVMeOFSubsystemByNQN finds an NVMe-oF subsystem by NQN.
// Returns nil, nil if not found.
func (c *Client) GetNVMeOFSubsystemByNQN(ctx context.Context, nqn string) (*NVMeOFSubsystem, error) {
	klog.V(4).Infof("Looking up NVMe-oF subsystem by NQN: %s", nqn)

	subsystems, err := c.ListNVMeOFSubsystems(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list NVMe-oF subsystems for NQN lookup: %w", err)
	}

	for i := range subsystems {
		if subsystems[i].NQN == nqn {
			klog.V(4).Infof("Found NVMe-oF subsystem %s for NQN %s", subsystems[i].ID, nqn)
			return &subsystems[i], nil
		}
	}

	klog.V(4).Infof("No NVMe-oF subsystem found with NQN %s", nqn)
	return nil, nil //nolint:nilnil // nil, nil indicates "not found"
}

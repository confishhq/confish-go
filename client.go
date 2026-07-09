package confish

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the production confish API base URL.
const DefaultBaseURL = "https://confi.sh"

// Options configure a Client.
type Options struct {
	// EnvID is the environment identifier (the bit after /c/ in the API URL).
	EnvID string
	// APIKey is the environment's bearer token, prefixed with "confish_sk_".
	APIKey string
	// BaseURL overrides the API base URL. Defaults to DefaultBaseURL.
	BaseURL string
	// HTTPClient overrides the underlying *http.Client. Defaults to a client with a 30s timeout.
	HTTPClient *http.Client
	// UserAgent overrides the User-Agent header. Defaults to "confish-go".
	UserAgent string
	// MaxRetries is the number of retry attempts beyond the initial request for 429/5xx
	// responses. Defaults to 2.
	MaxRetries int
	// MaxRetryDelay caps the delay between retries (e.g. when honoring Retry-After).
	// Defaults to 30 seconds.
	MaxRetryDelay time.Duration
}

// Client is the entry point to the confish API.
type Client struct {
	envID         string
	apiKey        string
	baseURL       string
	http          *http.Client
	userAgent     string
	maxRetries    int
	maxRetryDelay time.Duration

	// Config exposes the typed configuration API.
	Config *Config
	// Actions exposes the action management API.
	Actions *Actions
	// Logs exposes the log ingestion API.
	Logs *Logs
}

// New constructs a Client with the given options.
func New(opts Options) (*Client, error) {
	if opts.EnvID == "" {
		return nil, fmt.Errorf("confish: EnvID is required")
	}
	if opts.APIKey == "" {
		return nil, fmt.Errorf("confish: APIKey is required")
	}
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "confish-go"
	}
	if opts.MaxRetries == 0 {
		opts.MaxRetries = 2
	}
	if opts.MaxRetryDelay == 0 {
		opts.MaxRetryDelay = 30 * time.Second
	}

	c := &Client{
		envID:         opts.EnvID,
		apiKey:        opts.APIKey,
		baseURL:       strings.TrimRight(opts.BaseURL, "/"),
		http:          opts.HTTPClient,
		userAgent:     opts.UserAgent,
		maxRetries:    opts.MaxRetries,
		maxRetryDelay: opts.MaxRetryDelay,
	}
	c.Config = &Config{client: c}
	c.Actions = &Actions{client: c}
	c.Logs = &Logs{client: c}
	return c, nil
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	url := c.baseURL + path

	var rawBody []byte
	if body != nil {
		var err error
		rawBody, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("confish: encode request body: %w", err)
		}
	}

	for attempt := 0; ; attempt++ {
		var bodyReader io.Reader
		if rawBody != nil {
			bodyReader = bytes.NewReader(rawBody)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
		if err != nil {
			return fmt.Errorf("confish: build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.userAgent)
		if rawBody != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &NetworkError{URL: url, Err: err}
		}

		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if out == nil || len(respBody) == 0 {
				return nil
			}
			if err := json.Unmarshal(respBody, out); err != nil {
				return fmt.Errorf("confish: decode response: %w", err)
			}
			return nil
		}

		apiErr := errorFromResponse(resp.StatusCode, respBody, resp.Header)
		if !c.shouldRetry(attempt, apiErr) {
			return apiErr
		}

		delay := c.retryDelay(attempt, apiErr)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

func (c *Client) shouldRetry(attempt int, err error) bool {
	if attempt >= c.maxRetries {
		return false
	}
	var rl *RateLimitError
	if errors.As(err, &rl) {
		return true
	}
	var se *ServerError
	if errors.As(err, &se) {
		return true
	}
	return false
}

func (c *Client) retryDelay(attempt int, err error) time.Duration {
	var rl *RateLimitError
	if errors.As(err, &rl) && rl.RetryAfter > 0 {
		d := time.Duration(rl.RetryAfter) * time.Second
		if d > c.maxRetryDelay {
			return c.maxRetryDelay
		}
		return d
	}
	d := time.Duration(math.Pow(2, float64(attempt))) * time.Second
	if d > c.maxRetryDelay {
		return c.maxRetryDelay
	}
	return d
}

func errorFromResponse(status int, body []byte, headers http.Header) error {
	base := &APIError{StatusCode: status, Body: body, Message: extractMessage(body)}

	switch {
	case status == http.StatusUnauthorized:
		return &AuthError{APIError: base}
	case status == http.StatusForbidden:
		return &ForbiddenError{APIError: base}
	case status == http.StatusNotFound:
		return &NotFoundError{APIError: base}
	case status == http.StatusConflict:
		return &ConflictError{APIError: base}
	case status == http.StatusUnprocessableEntity:
		return &ValidationError{APIError: base, Errors: extractValidationErrors(body)}
	case status == http.StatusTooManyRequests:
		return &RateLimitError{
			APIError:   base,
			RetryAfter: parseIntHeader(headers.Get("Retry-After")),
			Limit:      parseIntHeader(headers.Get("X-RateLimit-Limit")),
			Remaining:  parseIntHeader(headers.Get("X-RateLimit-Remaining")),
		}
	case status >= 500:
		return &ServerError{APIError: base}
	}
	return base
}

func extractMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var probe struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return ""
	}
	if probe.Error != "" {
		return probe.Error
	}
	return probe.Message
}

func extractValidationErrors(body []byte) map[string][]string {
	var probe struct {
		Errors map[string][]string `json:"errors"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil
	}
	return probe.Errors
}

func parseIntHeader(s string) int {
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

package clients

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

var debugHTTP bool

func SetDebugHTTP(enabled bool) {
	debugHTTP = enabled
}

type HTTPClient struct {
	baseURL    string
	httpClient *http.Client
	headers    map[string]string
	editor     func(*http.Request) error
}

func NewHTTPClient(baseURL string, timeout time.Duration, headers map[string]string) *HTTPClient {
	return NewHTTPClientWithEditor(baseURL, timeout, headers, nil)
}

func NewHTTPClientWithEditor(baseURL string, timeout time.Duration, headers map[string]string, editor func(*http.Request) error) *HTTPClient {
	return &HTTPClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: timeout,
		},
		headers: headers,
		editor:  editor,
	}
}

func (c *HTTPClient) DoJSON(method, path string, query map[string]string, body interface{}, out interface{}) error {
	u, err := url.Parse(c.baseURL + path)
	if err != nil {
		return err
	}

	q := u.Query()
	for k, v := range query {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()

	var bodyReader io.Reader
	var requestBody []byte
	if body != nil {
		raw, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			return marshalErr
		}
		requestBody = raw
		bodyReader = bytes.NewReader(raw)
	}

	req, err := http.NewRequest(method, u.String(), bodyReader)
	if err != nil {
		return err
	}

	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	if c.editor != nil {
		if err := c.editor(req); err != nil {
			return err
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if debugHTTP {
		logRequest(req, requestBody)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if debugHTTP {
		logResponse(resp, respBody)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s failed with status %d: %s", method, u.String(), resp.StatusCode, string(respBody))
	}

	if out == nil {
		return nil
	}
	if len(respBody) == 0 {
		return nil
	}

	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("failed to decode response from %s: %w", u.String(), err)
	}
	return nil
}

func logRequest(req *http.Request, body []byte) {
	_, _ = fmt.Fprintf(os.Stderr, ">>> HTTP REQUEST %s %s\n", req.Method, req.URL.String())
	logHeaders(req.Header)
	if len(body) == 0 {
		_, _ = fmt.Fprintln(os.Stderr, ">>> Body: <empty>")
	} else {
		_, _ = fmt.Fprintf(os.Stderr, ">>> Body: %s\n", string(body))
	}
}

func logResponse(resp *http.Response, body []byte) {
	_, _ = fmt.Fprintf(os.Stderr, "<<< HTTP RESPONSE %s %s %d\n", resp.Request.Method, resp.Request.URL.String(), resp.StatusCode)
	logHeaders(resp.Header)
	if len(body) == 0 {
		_, _ = fmt.Fprintln(os.Stderr, "<<< Body: <empty>")
	} else {
		_, _ = fmt.Fprintf(os.Stderr, "<<< Body: %s\n", string(body))
	}
}

func logHeaders(h http.Header) {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		_, _ = fmt.Fprintf(os.Stderr, "    %s: %s\n", k, strings.Join(h[k], ", "))
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultAPIBase is the Hetzner Cloud API. Override it with HCLOUD_API_BASE
// to point at a mock while trying the tool out.
const DefaultAPIBase = "https://api.hetzner.cloud/v1"

// Server is the slice of a Hetzner server this tool needs.
type Server struct {
	ID       int64
	Name     string
	Created  time.Time
	Type     string
	Location string
	Labels   map[string]string
}

// Client is a minimal Hetzner Cloud client. Deliberately stdlib-only: this
// tool writes to someone's infrastructure with a write-scoped token, and a
// dependency tree is a thing a reviewer would have to audit too.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func NewClient(baseURL, token string) *Client {
	if baseURL == "" {
		baseURL = DefaultAPIBase
	}
	return &Client{BaseURL: baseURL, Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// APIError is a non-2xx response.
type APIError struct {
	Code int
	Body string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("hetzner api: HTTP %d: %s", e.Code, e.Body)
}

type apiServer struct {
	ID      int64             `json:"id"`
	Name    string            `json:"name"`
	Created time.Time         `json:"created"`
	Labels  map[string]string `json:"labels"`
	// The live API stopped nesting the location under `datacenter`: servers
	// carry a top-level `location` now, like volumes always did. Captures and
	// older deployments still use the nested shape, so both stay decodable.
	// Reading only one of them yields an empty location, and an empty location
	// matches nothing in the price table — the server would be silently
	// skipped rather than loudly wrong, which is a bad way to lose a server.
	Location *struct {
		Name string `json:"name"`
	} `json:"location"`
	Datacenter *struct {
		Location *struct {
			Name string `json:"name"`
		} `json:"location"`
	} `json:"datacenter"`
	ServerType *struct {
		Name string `json:"name"`
	} `json:"server_type"`
}

func (a apiServer) toServer() Server {
	s := Server{ID: a.ID, Name: a.Name, Created: a.Created, Labels: a.Labels}
	if s.Labels == nil {
		s.Labels = map[string]string{}
	}
	if a.ServerType != nil {
		s.Type = a.ServerType.Name
	}
	switch {
	case a.Location != nil && a.Location.Name != "":
		s.Location = a.Location.Name
	case a.Datacenter != nil && a.Datacenter.Location != nil:
		s.Location = a.Datacenter.Location.Name
	}
	return s
}

// ListServers walks every page of GET /servers.
func (c *Client) ListServers(ctx context.Context) ([]Server, error) {
	var out []Server
	page := 1
	for {
		url := fmt.Sprintf("%s/servers?page=%d&per_page=50", c.BaseURL, page)
		var payload struct {
			Servers []apiServer `json:"servers"`
			Meta    struct {
				Pagination struct {
					NextPage *int `json:"next_page"`
				} `json:"pagination"`
			} `json:"meta"`
		}
		if err := c.do(ctx, http.MethodGet, url, nil, &payload); err != nil {
			return nil, err
		}
		for _, a := range payload.Servers {
			out = append(out, a.toServer())
		}
		if payload.Meta.Pagination.NextPage == nil {
			return out, nil
		}
		page = *payload.Meta.Pagination.NextPage
	}
}

// SetLabels writes a server's labels.
//
// PUT /servers/{id} REPLACES the whole label map — it does not merge. Callers
// must therefore pass the complete desired set, which is why every write path
// here starts from the server's current labels and adds to them. Sending only
// our two keys would delete the customer's cost-center labels, and they would
// find out from a cost report weeks later.
func (c *Client) SetLabels(ctx context.Context, id int64, labels map[string]string) error {
	body, err := json.Marshal(struct {
		Labels map[string]string `json:"labels"`
	}{Labels: labels})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/servers/%d", c.BaseURL, id)
	return c.do(ctx, http.MethodPut, url, body, nil)
}

func (c *Client) do(ctx context.Context, method, url string, body []byte, out any) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{Code: resp.StatusCode, Body: string(raw)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode %s: %w", url, err)
	}
	return nil
}

// AccountCurrency reports the billing currency GET /pricing declares for the
// authenticated account ("EUR"/"USD").
//
// Hetzner fixes this when the account is created and quotes every price in it.
// The bundled table holds EUR figures, so writing them onto a USD-billed
// account would be wrong by an exchange rate — and would look entirely
// reasonable while being wrong.
func (c *Client) AccountCurrency(ctx context.Context) (string, error) {
	var payload struct {
		Pricing struct {
			Currency string `json:"currency"`
		} `json:"pricing"`
	}
	if err := c.do(ctx, http.MethodGet, c.BaseURL+"/pricing", nil, &payload); err != nil {
		return "", err
	}
	return payload.Pricing.Currency, nil
}

package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

var identifierPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

type Client struct {
	baseURL  string
	database string
	table    string
	username string
	password string
	http     *http.Client
}

type ClientConfig struct {
	URL, Database, Table, Username, Password string
	HTTPClient                               *http.Client
}

func NewClient(config ClientConfig) (*Client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(config.URL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("invalid ClickHouse URL %q", config.URL)
	}
	database := strings.TrimSpace(config.Database)
	table := strings.TrimSpace(config.Table)
	if !identifierPattern.MatchString(database) || !identifierPattern.MatchString(table) {
		return nil, errors.New("ClickHouse database and table must be safe identifiers")
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	return &Client{
		baseURL: baseURL, database: database, table: table,
		username: config.Username, password: config.Password, http: httpClient,
	}, nil
}

func (c *Client) Ping(ctx context.Context) error {
	query := url.Values{"query": {"SELECT 1"}}
	response, err := c.do(ctx, query, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return clickHouseError(response)
	}
	return nil
}

func (c *Client) Insert(ctx context.Context, event PaymentEvent) error {
	if event.EventID == uuid.Nil {
		return fmt.Errorf("%w: event ID is required", ErrInvalidEvent)
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal ClickHouse event: %w", err)
	}
	raw = append(raw, '\n')
	query := url.Values{
		"database":                   {c.database},
		"query":                      {"INSERT INTO `" + c.table + "` FORMAT JSONEachRow"},
		"date_time_input_format":     {"best_effort"},
		"insert_deduplication_token": {event.EventID.String()},
	}
	response, err := c.do(ctx, query, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("insert ClickHouse payment event: %w", clickHouseError(response))
	}
	return nil
}

func (c *Client) do(ctx context.Context, query url.Values, body io.Reader) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/?"+query.Encode(), body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/x-ndjson")
	}
	if c.username != "" {
		request.SetBasicAuth(c.username, c.password)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("ClickHouse request: %w", err)
	}
	return response, nil
}

func clickHouseError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	return fmt.Errorf("status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
}

package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const DefaultMapping = `{
  "settings": {
    "number_of_shards": 1,
    "number_of_replicas": 0,
    "index.mapping.total_fields.limit": 64
  },
  "mappings": {
    "dynamic": "strict",
    "properties": {
      "payment_id": {"type": "keyword"},
      "user_id": {"type": "keyword"},
      "amount": {"type": "long"},
      "currency": {"type": "keyword"},
      "description": {"type": "text", "fields": {"keyword": {"type": "keyword", "ignore_above": 256}}},
      "status": {"type": "keyword"},
      "provider_reference": {"type": "keyword"},
      "failure_reason": {"type": "text"},
      "aggregate_version": {"type": "long"},
      "created_at": {"type": "date"},
      "updated_at": {"type": "date"},
      "last_event_id": {"type": "keyword"},
      "last_event_type": {"type": "keyword"}
    }
  }
}`

type Client struct {
	baseURL  string
	index    string
	alias    string
	username string
	password string
	http     *http.Client
}

type ClientConfig struct {
	URL, Index, Alias, Username, Password string
	HTTPClient                            *http.Client
}

func NewClient(config ClientConfig) (*Client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(config.URL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("invalid Elasticsearch URL %q", config.URL)
	}
	index := strings.TrimSpace(config.Index)
	alias := strings.TrimSpace(config.Alias)
	if index == "" || alias == "" || index == alias {
		return nil, errors.New("Elasticsearch index and distinct alias are required")
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	return &Client{baseURL: baseURL, index: index, alias: alias, username: config.Username, password: config.Password, http: httpClient}, nil
}

func (c *Client) Ping(ctx context.Context) error {
	response, err := c.do(ctx, http.MethodGet, "/_cluster/health?local=true", nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return expect(response, http.StatusOK)
}

func (c *Client) EnsureIndex(ctx context.Context) error {
	response, err := c.do(ctx, http.MethodHead, "/"+url.PathEscape(c.index), nil)
	if err != nil {
		return err
	}
	status := response.StatusCode
	response.Body.Close()
	if status == http.StatusNotFound {
		response, err = c.do(ctx, http.MethodPut, "/"+url.PathEscape(c.index), strings.NewReader(DefaultMapping))
		if err != nil {
			return err
		}
		if err := expectIndexCreated(response); err != nil {
			response.Body.Close()
			return fmt.Errorf("create Elasticsearch index: %w", err)
		}
		response.Body.Close()
	} else if status != http.StatusOK {
		return fmt.Errorf("inspect Elasticsearch index: status %d", status)
	}

	response, err = c.do(ctx, http.MethodHead, "/_alias/"+url.PathEscape(c.alias), nil)
	if err != nil {
		return err
	}
	status = response.StatusCode
	response.Body.Close()
	if status == http.StatusOK {
		return nil
	}
	if status != http.StatusNotFound {
		return fmt.Errorf("inspect Elasticsearch alias: status %d", status)
	}
	actions := map[string]any{"actions": []any{map[string]any{"add": map[string]any{"index": c.index, "alias": c.alias, "is_write_index": true}}}}
	raw, _ := json.Marshal(actions)
	response, err = c.do(ctx, http.MethodPost, "/_aliases", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if err := expect(response, http.StatusOK); err != nil {
		return fmt.Errorf("create Elasticsearch alias: %w", err)
	}
	return nil
}

func expectIndexCreated(response *http.Response) error {
	if response.StatusCode == http.StatusOK {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	// The indexer and query service may bootstrap concurrently. Elasticsearch
	// reports the losing create request as 400 even though the desired index now
	// exists, so that one narrowly identified response is safe to accept.
	if response.StatusCode == http.StatusBadRequest && strings.Contains(string(body), "resource_already_exists_exception") {
		return nil
	}
	return fmt.Errorf("status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
}

func (c *Client) Index(ctx context.Context, document PaymentDocument) error {
	if document.PaymentID == uuid.Nil || document.AggregateVersion < 1 {
		return fmt.Errorf("%w: invalid document identity or version", ErrInvalidEvent)
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("marshal payment search document: %w", err)
	}
	path := "/" + url.PathEscape(c.alias) + "/_doc/" + document.PaymentID.String() +
		"?version=" + strconv.FormatInt(document.AggregateVersion, 10) + "&version_type=external"
	response, err := c.do(ctx, http.MethodPut, path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		// Duplicate delivery and out-of-order lifecycle events are already
		// represented by a document with an equal or newer aggregate version.
		return nil
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		return fmt.Errorf("index payment document: %w", responseError(response))
	}
	return nil
}

func (c *Client) Search(ctx context.Context, query Query) (Result, error) {
	body, err := buildSearchRequest(query)
	if err != nil {
		return Result{}, err
	}
	raw, _ := json.Marshal(body)
	response, err := c.do(ctx, http.MethodPost, "/"+url.PathEscape(c.alias)+"/_search", bytes.NewReader(raw))
	if err != nil {
		return Result{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("search payments: %w", responseError(response))
	}
	var payload struct {
		Hits struct {
			Total struct {
				Value int64 `json:"value"`
			} `json:"total"`
			Hits []struct {
				Source PaymentDocument   `json:"_source"`
				Sort   []json.RawMessage `json:"sort"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return Result{}, fmt.Errorf("decode Elasticsearch response: %w", err)
	}
	result := Result{Total: payload.Hits.Total.Value, Items: make([]PaymentDocument, 0, min(query.Limit, len(payload.Hits.Hits)))}
	for index, hit := range payload.Hits.Hits {
		if index == query.Limit {
			break
		}
		result.Items = append(result.Items, hit.Source)
	}
	if len(payload.Hits.Hits) > query.Limit && query.Limit > 0 {
		last := payload.Hits.Hits[query.Limit-1]
		cursor, err := cursorFromSort(last.Sort)
		if err != nil {
			return Result{}, err
		}
		result.NextCursor = EncodeCursor(cursor)
	}
	return result, nil
}

func buildSearchRequest(query Query) (map[string]any, error) {
	if query.Limit < 1 || query.Limit > 100 {
		return nil, fmt.Errorf("%w: limit must be between 1 and 100", ErrInvalidQuery)
	}
	filters := make([]any, 0, 7)
	if query.UserID != nil {
		filters = append(filters, map[string]any{"term": map[string]any{"user_id": query.UserID.String()}})
	}
	if len(query.Statuses) == 1 {
		filters = append(filters, map[string]any{"term": map[string]any{"status": query.Statuses[0]}})
	} else if len(query.Statuses) > 1 {
		filters = append(filters, map[string]any{"terms": map[string]any{"status": query.Statuses}})
	}
	if query.Currency != "" {
		filters = append(filters, map[string]any{"term": map[string]any{"currency": strings.ToUpper(query.Currency)}})
	}
	if query.AmountMin != nil || query.AmountMax != nil {
		rangeValue := map[string]any{}
		if query.AmountMin != nil {
			rangeValue["gte"] = *query.AmountMin
		}
		if query.AmountMax != nil {
			rangeValue["lte"] = *query.AmountMax
		}
		filters = append(filters, map[string]any{"range": map[string]any{"amount": rangeValue}})
	}
	if query.CreatedFrom != nil || query.CreatedTo != nil {
		rangeValue := map[string]any{}
		if query.CreatedFrom != nil {
			rangeValue["gte"] = query.CreatedFrom.UTC().Format(time.RFC3339Nano)
		}
		if query.CreatedTo != nil {
			rangeValue["lte"] = query.CreatedTo.UTC().Format(time.RFC3339Nano)
		}
		filters = append(filters, map[string]any{"range": map[string]any{"created_at": rangeValue}})
	}
	must := make([]any, 0, 1)
	if strings.TrimSpace(query.Text) != "" {
		must = append(must, map[string]any{"simple_query_string": map[string]any{
			"query": query.Text, "fields": []string{"description^3", "provider_reference", "failure_reason"}, "default_operator": "and",
		}})
	}
	request := map[string]any{
		"size":             query.Limit + 1,
		"track_total_hits": true,
		"sort": []any{
			// Elasticsearch otherwise serializes a date sort value as epoch
			// milliseconds. Requesting an explicit format keeps the opaque cursor
			// lossless and consistent with the RFC3339 search_after value below.
			map[string]any{"created_at": map[string]any{
				"order": "desc", "format": "strict_date_optional_time_nanos",
			}},
			map[string]any{"payment_id": map[string]any{"order": "desc"}},
		},
		"query": map[string]any{"bool": map[string]any{"filter": filters, "must": must}},
	}
	if query.Cursor != nil {
		request["search_after"] = []any{query.Cursor.CreatedAt.UTC().Format(time.RFC3339Nano), query.Cursor.PaymentID.String()}
	}
	return request, nil
}

func cursorFromSort(values []json.RawMessage) (Cursor, error) {
	if len(values) != 2 {
		return Cursor{}, fmt.Errorf("decode Elasticsearch cursor: expected two sort values")
	}
	var createdAtValue, paymentIDValue string
	if err := json.Unmarshal(values[0], &createdAtValue); err != nil {
		return Cursor{}, fmt.Errorf("decode Elasticsearch cursor timestamp: %w", err)
	}
	if err := json.Unmarshal(values[1], &paymentIDValue); err != nil {
		return Cursor{}, fmt.Errorf("decode Elasticsearch cursor payment ID: %w", err)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, createdAtValue)
	if err != nil {
		return Cursor{}, fmt.Errorf("decode Elasticsearch cursor timestamp: %w", err)
	}
	paymentID, err := uuid.Parse(paymentIDValue)
	if err != nil {
		return Cursor{}, fmt.Errorf("decode Elasticsearch cursor payment ID: %w", err)
	}
	return Cursor{CreatedAt: createdAt, PaymentID: paymentID}, nil
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.username != "" {
		request.SetBasicAuth(c.username, c.password)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("Elasticsearch %s %s: %w", method, path, err)
	}
	return response, nil
}

func expect(response *http.Response, expected int) error {
	if response.StatusCode != expected {
		return responseError(response)
	}
	return nil
}

func responseError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	return fmt.Errorf("status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
}

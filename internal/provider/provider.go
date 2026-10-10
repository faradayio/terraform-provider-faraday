// Package provider is the Faraday Terraform provider.
//
// Schemas in *_resource_gen.go are generated. CRUD and terraform query listing
// are hand-written and shared by every resource type in resourceDefs.
package provider

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/list"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ provider.ProviderWithListResources = (*FaradayProvider)(nil)

type FaradayProvider struct{ version string }

const defaultBaseURL = "https://api.faraday.ai/v1"

type providerConfig struct {
	ApiKey   types.String `tfsdk:"api_key"`
	BaseUrl  types.String `tfsdk:"base_url"`
	Insecure types.Bool   `tfsdk:"insecure"`
}

func (cfg providerConfig) baseURL() string {
	if cfg.BaseUrl.IsNull() || cfg.BaseUrl.IsUnknown() || cfg.BaseUrl.ValueString() == "" {
		return defaultBaseURL
	}
	return strings.TrimRight(cfg.BaseUrl.ValueString(), "/")
}

// client is the tiny REST client the resources share.
type client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

func New(version string) func() provider.Provider {
	return func() provider.Provider { return &FaradayProvider{version: version} }
}

func (p *FaradayProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "faraday"
	resp.Version = p.version
}

func (p *FaradayProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description:         "Manage Faraday resources.",
		MarkdownDescription: "Manage Faraday resources.",
		Attributes: map[string]schema.Attribute{
			"api_key": schema.StringAttribute{
				Required:            true,
				Sensitive:           true,
				Description:         "Faraday API key.",
				MarkdownDescription: "Faraday API key.",
			},
			"base_url": schema.StringAttribute{
				Optional:            true,
				Description:         "Faraday API base URL. Defaults to https://api.faraday.ai/v1.",
				MarkdownDescription: "Faraday API base URL. Defaults to https://api.faraday.ai/v1.",
			},
			"insecure": schema.BoolAttribute{
				Optional:            true,
				Description:         "Skip TLS certificate verification. For local development against a self-signed certificate only.",
				MarkdownDescription: "Skip TLS certificate verification. For local development against a self-signed certificate only.",
			},
		},
	}
}

func (p *FaradayProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerConfig
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tr := &http.Transport{}
	if cfg.Insecure.ValueBool() {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	c := &client{
		apiKey:  cfg.ApiKey.ValueString(),
		baseURL: cfg.baseURL(),
		http:    &http.Client{Transport: tr, Timeout: 60 * time.Second},
	}
	resp.ResourceData = c
	resp.DataSourceData = c
	resp.ListResourceData = c
}

func (p *FaradayProvider) ListResources(_ context.Context) []func() list.ListResource {
	return allListResources()
}

func (p *FaradayProvider) Resources(_ context.Context) []func() resource.Resource {
	return allResources()
}

func (p *FaradayProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}

// apiResponse is one HTTP exchange with the Faraday API, before decoding.
type apiResponse struct {
	method string
	path   string
	status int
	body   []byte
}

func (r *apiResponse) failure() error {
	if r.status == http.StatusNotFound {
		return errNotFound
	}
	if r.status >= 300 {
		return &apiError{
			method: r.method, path: r.path,
			status: r.status, body: string(r.body),
		}
	}
	return nil
}

func (c *client) send(ctx context.Context, method, path string, body any) (*apiResponse, error) {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = strings.NewReader(string(buf))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return &apiResponse{method: method, path: path, status: res.StatusCode, body: raw}, nil
}

// do issues one API call and decodes a JSON object response.
func (c *client) do(ctx context.Context, method, path string, body any) (map[string]any, error) {
	res, err := c.send(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	if err := res.failure(); err != nil {
		return nil, err
	}
	if len(res.body) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(res.body, &out); err != nil {
		return nil, fmt.Errorf("%s %s: decoding response: %w", method, path, err)
	}
	return out, nil
}

// list decodes a collection response. Faraday list endpoints return a JSON array.
func (c *client) list(ctx context.Context, path string) ([]map[string]any, error) {
	res, err := c.send(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	if err := res.failure(); err != nil {
		return nil, err
	}
	var items []map[string]any
	if err := json.Unmarshal(res.body, &items); err != nil {
		return nil, fmt.Errorf("GET %s: decoding list: %w", path, err)
	}
	return items, nil
}

var errNotFound = fmt.Errorf("not found")

// apiError is a non-2xx response. It keeps the status and body separate so a
// caller can branch on them -- Delete needs to recognise one specific
// validation failure -- rather than matching against a formatted string.
type apiError struct {
	method string
	path   string
	status int
	body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.method, e.path, e.status, e.body)
}

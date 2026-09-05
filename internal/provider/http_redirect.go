package provider

import "net/http"

// providerHTTPClient owns the redirect policy without mutating a caller's
// shared client. API redirects are configuration errors, including same-origin
// redirects: even those may convert POST to GET or redirect again off-origin.
// Preserve transport, jar and timeouts; no caller policy can weaken this bound.
func providerHTTPClient(base *http.Client) *http.Client {
	client := http.Client{}
	if base != nil {
		client = *base
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = providerNoRedirectTransport{base: transport}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &client
}

type providerNoRedirectTransport struct{ base http.RoundTripper }

func (t providerNoRedirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil || resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return resp, err
	}
	// Suppress Location before net/http even parses it. Otherwise a malformed
	// Location can put secrets from that header into a url.Error before the
	// CheckRedirect callback is reached. Do not mutate the transport's response.
	blocked := *resp
	blocked.Header = resp.Header.Clone()
	blocked.Header.Del("Location")
	return &blocked, nil
}

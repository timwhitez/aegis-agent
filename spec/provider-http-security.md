# Provider HTTP redirect boundary

The shared `JSONClient` never follows an HTTP redirect. This applies to 301,
302, 303, 307 and 308, including relative and same-origin destinations. Operators
must configure the canonical API endpoint. This policy avoids both credential
header forwarding and replay of a prompt/body; stripping `Authorization` alone
is not sufficient for `x-api-key`, `x-goog-api-key` or body content.

Each call copies the supplied `http.Client` and preserves its transport,
timeouts and cookie jar. It does not mutate a shared client. The provider policy
is an upper bound: a supplied redirect callback cannot opt into redirects. The
transport response is copied before removing `Location`, so net/http cannot put
an invalid or secret-bearing Location into a parsing error. Redirect bodies are
closed without being read or persisted. A redirect is a non-retryable
`invalid_request` with its original status and a fixed diagnostic.

This does not constrain a custom RoundTripper that performs additional network
requests itself; supplied transports are trusted application code. It does not
change the existing timeout, retry, response-size or response-decoding contract
for non-redirect responses.

Regression coverage is in `internal/provider/http_redirect_test.go`: the matrix
asserts zero destination calls, not merely that an error was returned. It also
covers malformed Location, no retry, response ownership, concurrent shared-client
use and a real local HTTP 307 origin/sink pair. All credentials are fixtures.

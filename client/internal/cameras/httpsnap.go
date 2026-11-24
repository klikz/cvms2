package cameras

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"cvms/client/internal/digest"
)

var snapHTTP = &http.Client{Timeout: 6 * time.Second}

// httpSnapshot fetches a JPEG from a camera HTTP snapshot URL, answering
// Basic or Digest authentication challenges.
func httpSnapshot(ctx context.Context, rawURL, user, pass string) ([]byte, error) {
	resp, err := snapGet(ctx, rawURL, "")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && user != "" {
		chal := digest.Pick(resp.Header.Values("WWW-Authenticate"))
		resp.Body.Close()
		u, _ := url.Parse(rawURL)
		auth, err := digest.Header(chal, http.MethodGet, u.RequestURI(), user, pass)
		if err != nil {
			return nil, err
		}
		resp, err = snapGet(ctx, rawURL, auth)
		if err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("snapshot http %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return nil, errors.New("snapshot is not a JPEG")
	}
	return b, nil
}

func snapGet(ctx context.Context, u, auth string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	return snapHTTP.Do(req)
}

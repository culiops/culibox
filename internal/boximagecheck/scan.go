package boximagecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
)

// scanAuthor is the only login whose issue counts: the weekly scan opens it as the
// Actions bot, and a person may title an issue the same.
const scanAuthor = "github-actions[bot]"

// scanTitle is what box-image-scan.yml writes: "<prefix>: <n> HIGH or CRITICAL".
var scanTitle = regexp.MustCompile(`^Box image: vulnerabilities with a fix: ([0-9]{1,6}) HIGH or CRITICAL$`)

// fetchScan asks GitHub, anonymously, for the bot's open issues and reads the weekly
// scan's number and count from the one that fits. No such issue is Scan{0, 0}: none
// is open, which is an answer, not a failure. It sets no Authorization header,
// ever: the repo is public and a token would only widen what a leak can reach.
func (k Checker) fetchScan(ctx context.Context) (*Scan, error) {
	q := url.Values{"state": {"open"}, "creator": {scanAuthor}, "per_page": {"100"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.GitHubAPI+"/repos/culiops/culibox/issues?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", userAgent)
	resp, err := k.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("issues: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBody {
		return nil, errors.New("issues: answer too large")
	}
	var issues []struct {
		Number      int                     `json:"number"`
		Title       string                  `json:"title"`
		User        *struct{ Login string } `json:"user"`
		PullRequest json.RawMessage         `json:"pull_request"`
	}
	if err := json.Unmarshal(body, &issues); err != nil {
		return nil, err
	}
	for _, is := range issues {
		// The list holds pull requests too; only an issue of the bot's is the scan's.
		if len(is.PullRequest) != 0 && string(is.PullRequest) != "null" {
			continue
		}
		if is.User == nil || is.User.Login != scanAuthor || is.Number < 1 || is.Number > maxScanField {
			continue
		}
		m := scanTitle.FindStringSubmatch(is.Title)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		return &Scan{Number: is.Number, Count: n}, nil
	}
	return &Scan{}, nil
}

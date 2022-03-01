package api

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	url "net/url"

	"gitlab.com/NebulousLabs/errors"
)

// Client is a helper struct that gets initialised using a portal url. It
// exposes API methods and abstracts the response handling.
type Client struct {
	staticPortalURL string
}

// NewClient returns a new Client instance for given portal url.
func NewClient(portalURL string) *Client {
	return &Client{staticPortalURL: portalURL}
}

// BlocklistGET calls the `/portal/blocklist` endpoint with given parameters
func (c *Client) BlocklistGET(offset int) (*BlocklistGET, error) {
	// set url values
	values := url.Values{}
	values.Set("offset", fmt.Sprint(offset))
	values.Set("sort", "desc")
	query := values.Encode()

	// create the request
	url := fmt.Sprintf("%s/portal/blocklist?%s", c.staticPortalURL, query)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, errors.AddContext(err, fmt.Sprintf("failed to build blocklist request for portal %s", c.staticPortalURL))
	}

	// set headers and execute the request
	req.Header.Set("User-Agent", "Sia-Agent")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, errors.AddContext(err, fmt.Sprintf("failed to fetch blocklist for portal %s", c.staticPortalURL))
	}
	defer res.Body.Close()

	// check status code
	if res.StatusCode != 200 {
		return nil, errors.AddContext(err, fmt.Sprintf("unexpected status code %d from portal %s", res.StatusCode, c.staticPortalURL))
	}

	// handle the response
	data, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, errors.AddContext(err, fmt.Sprintf("failed to read response body from portal %s", c.staticPortalURL))
	}
	var blg BlocklistGET
	err = json.Unmarshal(data, &blg)
	if err != nil {
		return nil, errors.AddContext(err, fmt.Sprintf("failed to unmarshal blocklist response from portal %s", c.staticPortalURL))
	}
	return &blg, nil
}

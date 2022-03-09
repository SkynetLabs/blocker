package api

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	url "net/url"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/node/api"
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
	query := url.Values{}
	query.Set("offset", fmt.Sprint(offset))
	query.Set("sort", "desc")

	// execute the get request
	var blg BlocklistGET
	err := c.get("/skynet/portal/blocklist", query, &blg)
	if err != nil {
		return nil, errors.AddContext(err, fmt.Sprintf("failed to fetch blocklist for portal %s", c.staticPortalURL))
	}

	return &blg, nil
}

// get is a helper function that executes a GET request on the given endpoint
// with the provided query values. The response will get unmarshaled into the
// given response object.
func (c *Client) get(endpoint string, query url.Values, obj interface{}) error {
	// create the request
	url := fmt.Sprintf("%s%s?%s", c.staticPortalURL, endpoint, query.Encode())
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return errors.AddContext(err, "failed to create request")
	}

	// set headers and execute the request
	req.Header.Set("User-Agent", "Sia-Agent")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer drainAndClose(res.Body)

	// return an error if the status code is not in the 200s
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("GET request to '%s' with status %d error %v", endpoint, res.StatusCode, readAPIError(res.Body))
	}

	// handle the response body
	err = json.NewDecoder(res.Body).Decode(obj)
	if err != nil {
		return err
	}
	return nil
}

// drainAndClose reads rc until EOF and then closes it. drainAndClose should
// always be called on HTTP response bodies, because if the body is not fully
// read, the underlying connection can't be reused.
func drainAndClose(rc io.ReadCloser) {
	io.Copy(ioutil.Discard, rc)
	rc.Close()
}

// readAPIError decodes and returns an api.Error.
func readAPIError(r io.Reader) error {
	var apiErr api.Error

	err := json.NewDecoder(r).Decode(&apiErr)
	if err != nil {
		return errors.AddContext(err, "could not read error response")
	}

	return apiErr
}

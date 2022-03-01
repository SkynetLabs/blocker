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
	query := url.Values{}
	query.Set("offset", fmt.Sprint(offset))
	query.Set("sort", "desc")

	// execute the get request
	var blg BlocklistGET
	err := c.get("/portal/blocklist", query, &blg)
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
	defer res.Body.Close()

	// handle the response
	data, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return err
	}

	// return an error if the status code is not in the 200s
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("GET request to '%s' with status %d, response body: %v", endpoint, res.StatusCode, string(data))
	}

	// unmarshal the body into the given object
	err = json.Unmarshal(data, obj)
	if err != nil {
		return err
	}
	return nil
}

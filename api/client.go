package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"

	skyapi "gitlab.com/SkynetLabs/skyd/node/api"
	"gitlab.com/SkynetLabs/skyd/skymodules"

	"github.com/SkynetLabs/blocker/database"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/node/api"
)

const (
	// clientDefaultTimeout is the timeout of the http calls to in seconds
	clientDefaultTimeout = "30"
)

type (
	// SkydClient is a helper struct that gets initialised using a portal url.
	// It exposes API methods and abstracts the response handling.
	SkydClient struct {
		staticDefaultHeaders http.Header
		staticPortalURL      string
	}

	// BlockResponse is the response object returned by the Skyd API's block
	// endpoint
	BlockResponse struct {
		Invalids []InvalidInput `json:"invalids"`
	}

	// DaemonReadyResponse is the response object returned by the Skyd API's
	// ready endpoint
	DaemonReadyResponse struct {
		Ready     bool `json:"ready"`
		Consensus bool `json:"consensus"`
		Gateway   bool `json:"gateway"`
		Renter    bool `json:"renter"`
	}

	// InvalidInput is a struct that wraps the invalid input along with an error
	// string indicating why it was deemed invalid
	InvalidInput struct {
		Input string `json:"input"`
		Error string `json:"error"`
	}

	// resolveResponse is the response object returned by the Skyd API's resolve
	// endpoint
	resolveResponse struct {
		Skylink string `json:"skylink"`
	}
)

// NewSkydClient returns a client that has the default user-agent set.
func NewSkydClient(portalURL, apiPassword string) *SkydClient {
	headers := http.Header{}
	if apiPassword != "" {
		encoded := base64.StdEncoding.EncodeToString([]byte(":" + apiPassword))
		headers.Set("Authorization", fmt.Sprintf("Basic %s", encoded))
	}
	headers.Set("User-Agent", "Sia-Agent")
	return NewCustomSkydClient(portalURL, headers)
}

// NewCustomSkydClient returns a new SkydClient instance for given portal url
// and lets you pass a set of headers that will be set on every request.
func NewCustomSkydClient(portalURL string, headers http.Header) *SkydClient {
	headers.Set("User-Agent", "Sia-Agent")
	return &SkydClient{
		staticDefaultHeaders: headers,
		staticPortalURL:      portalURL,
	}
}

// InvalidHashes is a helper method that converts the list of invalid inputs to
// an array of hashes.
func (br *BlockResponse) InvalidHashes() ([]database.Hash, error) {
	if len(br.Invalids) == 0 {
		return nil, nil
	}

	hashes := make([]database.Hash, len(br.Invalids))
	for i, invalid := range br.Invalids {
		var h database.Hash
		err := h.LoadString(invalid.Input)
		if err != nil {
			return nil, err
		}
		hashes[i] = h
	}
	return hashes, nil
}

// BlocklistGET calls the `/portal/blocklist` endpoint with given parameters
func (c *SkydClient) BlocklistGET(offset int) (*BlocklistGET, error) {
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

// BlockHashes will perform an API call to skyd to block the given hashes. It
// returns which hashes were blocked, which hashes were invalid and potentially
// an error.
func (c *SkydClient) BlockHashes(hashes []database.Hash) ([]database.Hash, []database.Hash, error) {
	// convert the hashes to strings
	adds := make([]string, len(hashes))
	for h, hash := range hashes {
		adds[h] = hash.String()
	}

	// build the post body
	reqBody, err := json.Marshal(skyapi.SkynetBlocklistPOST{
		Add:    adds,
		Remove: nil,
		IsHash: true,
	})
	if err != nil {
		return nil, nil, errors.AddContext(err, "failed to build request body")
	}
	body := bytes.NewBuffer(reqBody)

	// build the query parameters
	query := url.Values{}
	query.Add("timeout", clientDefaultTimeout)

	// execute the request
	var response BlockResponse
	err = c.post("/skynet/blocklist", query, body, &response)
	if err != nil {
		return nil, nil, errors.AddContext(err, "failed to execute POST request")
	}

	// parse the invalid hashes from the response
	invalids, err := response.InvalidHashes()
	if err != nil {
		return nil, nil, errors.AddContext(err, "failed to parse invalid hashes from skyd response")
	}

	return database.DiffHashes(hashes, invalids), invalids, nil
}

// ResolveSkylink will resolve the given skylink.
func (c *SkydClient) ResolveSkylink(skylink skymodules.Skylink) (skymodules.Skylink, error) {
	// no need to resolve the skylink if it's a v1 skylink
	if skylink.IsSkylinkV1() {
		return skylink, nil
	}

	// execute the request
	var response resolveResponse
	endpoint := fmt.Sprintf("/skynet/resolve/%s", skylink.String())
	err := c.get(endpoint, url.Values{}, &response)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "failed to execute GET request")
	}

	// check whether we resolved a valid skylink
	err = skylink.LoadString(response.Skylink)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "unable to load the resolved skylink")
	}
	return skylink, nil
}

// DaemonReady connects to the local skyd and checks its status.
// Returns true only if skyd is fully ready.
func (c *SkydClient) DaemonReady() bool {
	var response DaemonReadyResponse
	err := c.get("/daemon/ready", url.Values{}, &response)
	if err != nil {
		return false
	}

	return response.Ready &&
		response.Consensus &&
		response.Gateway &&
		response.Renter
}

// get is a helper function that executes a GET request on the given endpoint
// with the provided query values. The response will get unmarshaled into the
// given response object.
func (c *SkydClient) get(endpoint string, query url.Values, obj interface{}) error {
	// create the request
	queryString := query.Encode()
	url := fmt.Sprintf("%s%s", c.staticPortalURL, endpoint)
	if queryString != "" {
		url = fmt.Sprintf("%s%s?%s", c.staticPortalURL, endpoint, queryString)
	}

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
		return fmt.Errorf("GET request to '%s' with status %d error %v", url, res.StatusCode, readAPIError(res.Body))
	}

	// handle the response body
	err = json.NewDecoder(res.Body).Decode(obj)
	if err != nil {
		return err
	}
	return nil
}

// post is a helper function that executes a POST request on the given endpoint
// with the provided query values.
func (c *SkydClient) post(endpoint string, query url.Values, body io.Reader, obj interface{}) error {
	// create the request
	url := fmt.Sprintf("%s%s?%s", c.staticPortalURL, endpoint, query.Encode())
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		return errors.AddContext(err, "failed to create request")
	}

	// set headers and execute the request
	for k, v := range c.staticDefaultHeaders {
		req.Header.Set(k, v[0])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer drainAndClose(res.Body)

	// return an error if the status code is not in the 200s
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("GET request to '%s' with status %d error %v", url, res.StatusCode, readAPIError(res.Body))
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

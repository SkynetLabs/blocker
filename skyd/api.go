package skyd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"

	skyapi "gitlab.com/SkynetLabs/skyd/node/api"

	"github.com/SkynetLabs/blocker/database"
	"github.com/sirupsen/logrus"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

const (
	// skydTimeout is the timeout of the http calls to skyd in seconds
	skydTimeout = "30"
)

// API defines the skyd API interface. It's an interface for testing purposes,
// as this allows to easily mock it and alleviates the need for a skyd instance.
type API interface {
	// BlockHashes adds the given hashes to the block list. It returns which
	// hashes were blocked, which hashes were invalid and potentially an error.
	BlockHashes([]database.Hash) ([]database.Hash, []database.Hash, error)
	// IsSkydUp returns true if the skyd API instance is up.
	IsSkydUp() bool
	// ResolveSkylink tries to resolve the given skylink to a V1 skylink.
	ResolveSkylink(skymodules.Skylink) (skymodules.Skylink, error)
}

type (
	// api is a helper struct that exposes some methods that allow making skyd
	// API calls used by both the API and the blocker
	api struct {
		staticSkydHost        string
		staticSkydPort        int
		staticSkydAPIPassword string

		staticDB     *database.DB
		staticLogger *logrus.Logger
	}

	// blockResponse is the response object returned by the Skyd API's block
	// endpoint
	blockResponse struct {
		Invalids []invalidInput `json:"invalids"`
	}

	// invalidInput is a struct that wraps the invalid input along with an error
	// string indicating why it was deemed invalid
	invalidInput struct {
		Input string `json:"input"`
		Error string `json:"error"`
	}
)

// InvalidHashes is a helper method that converts the list of invalid inputs to
// an array of hashes.
func (br *blockResponse) InvalidHashes() ([]database.Hash, error) {
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

// NewAPI creates a new API instance.
func NewAPI(skydHost, skydPassword string, skydPort int, db *database.DB, logger *logrus.Logger) (API, error) {
	if db == nil {
		return nil, errors.New("no DB provided")
	}
	if logger == nil {
		return nil, errors.New("no logger provided")
	}

	return &api{
		staticSkydHost:        skydHost,
		staticSkydPort:        skydPort,
		staticSkydAPIPassword: skydPassword,

		staticDB:     db,
		staticLogger: logger,
	}, nil
}

// BlockHashes will perform an API call to skyd to block the given hashes. It
// returns which hashes were blocked, which hashes were invalid and potentially
// an error.
func (api *api) BlockHashes(hashes []database.Hash) ([]database.Hash, []database.Hash, error) {
	api.staticLogger.Debugf("blocking %v hashes", len(hashes))

	// convert the hashes to strings
	adds := make([]string, len(hashes))
	for h, hash := range hashes {
		adds[h] = hash.String()
	}

	// build the call to skyd.
	reqBody, err := json.Marshal(skyapi.SkynetBlocklistPOST{
		Add:    adds,
		Remove: nil,
		IsHash: true,
	})
	if err != nil {
		return nil, nil, errors.AddContext(err, "failed to build request body")
	}

	// execute the request
	url := fmt.Sprintf("http://%s:%d/skynet/blocklist?timeout=%s", api.staticSkydHost, api.staticSkydPort, skydTimeout)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, nil, errors.AddContext(err, "failed to build request to skyd")
	}
	req.Header.Set("User-Agent", "Sia-Agent")
	req.Header.Set("Authorization", api.staticAuthHeader())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, errors.AddContext(err, "failed to make request to skyd")
	}
	defer resp.Body.Close()

	// read the response body
	respBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, errors.AddContext(err, "failed to parse response body after a failed call to skyd")
	}

	// if the request failed return an error containing the response body
	if resp.StatusCode != http.StatusOK {
		return nil, nil, errors.New(fmt.Sprintf("call to skyd failed with status '%s' and response '%s'", resp.Status, string(respBody)))
	}

	// unmarshal the response
	var response blockResponse
	err = json.Unmarshal(respBody, &response)
	if err != nil {
		return nil, nil, errors.AddContext(err, "failed to parse unmarshal skyd response")
	}

	invalids, err := response.InvalidHashes()
	if err != nil {
		return nil, nil, errors.AddContext(err, "failed to parse invalid hashes from skyd response")
	}

	return database.DiffHashes(hashes, invalids), invalids, nil
}

// ResolveSkylink will resolve the given skylink.
func (api *api) ResolveSkylink(skylink skymodules.Skylink) (skymodules.Skylink, error) {
	// no need to resolve the skylink if it's a v1 skylink
	if skylink.IsSkylinkV1() {
		return skylink, nil
	}

	// build the request to resolve the skylink with skyd
	url := fmt.Sprintf("http://%s:%d/skynet/resolve/%s", api.staticSkydHost, api.staticSkydPort, skylink.String())
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "failed to build request to skyd")
	}

	// set headers and execute the request
	req.Header.Set("User-Agent", "Sia-Agent")
	req.Header.Set("Authorization", api.staticAuthHeader())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "failed to make request to skyd")
	}
	defer resp.Body.Close()

	// if the status code is not 200 OK, try and extract the error and return it
	if resp.StatusCode != http.StatusOK {
		errorResponse := struct {
			Message string `json:"message"`
		}{}
		if err := json.NewDecoder(resp.Body).Decode(&errorResponse); err != nil {
			return skymodules.Skylink{}, errors.AddContext(err, "unable to decode error response from skyd")
		}
		return skymodules.Skylink{}, errors.New(errorResponse.Message)
	}

	// decode the resolved skylink
	resolved := struct {
		Skylink string `json:"skylink"`
	}{}
	if err := json.NewDecoder(resp.Body).Decode(&resolved); err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "unable to decode response from skyd")
	}
	if err := skylink.LoadString(resolved.Skylink); err != nil {
		return skymodules.Skylink{}, errors.AddContext(err, "unable to load the resolved skylink")
	}
	return skylink, nil
}

// IsSkydUp connects to the local skyd and checks its status.
// Returns true only if skyd is fully ready.
func (api *api) IsSkydUp() bool {
	status := struct {
		Ready     bool
		Consensus bool
		Gateway   bool
		Renter    bool
	}{}
	url := fmt.Sprintf("http://%s:%d/daemon/ready", api.staticSkydHost, api.staticSkydPort)
	r, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		api.staticLogger.Error(err)
		return false
	}
	r.Header.Set("User-Agent", "Sia-Agent")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		api.staticLogger.Warnf("Failed to query skyd: %s", err.Error())
		return false
	}
	defer resp.Body.Close()
	err = json.NewDecoder(resp.Body).Decode(&status)
	if err != nil {
		api.staticLogger.Warnf("Bad body from skyd's /daemon/ready: %s", err.Error())
		return false
	}
	return status.Ready && status.Consensus && status.Gateway && status.Renter
}

// staticAuthHeader returns the value we need to set to the `Authorization`
// header in order to call `skyd`.
func (api *api) staticAuthHeader() string {
	return fmt.Sprintf("Basic %s", base64.StdEncoding.EncodeToString([]byte(":"+api.staticSkydAPIPassword)))
}

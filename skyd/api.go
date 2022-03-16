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
	// BlockHashes adds the given hashes to the block list.
	BlockHashes([]string) error
	// IsSkydUp returns true if the skyd API instance is up.
	IsSkydUp() bool
	// ResolveSkylink tries to resolve the given skylink to a V1 skylink.
	ResolveSkylink(skymodules.Skylink) (skymodules.Skylink, error)
}

// api is a helper struct that exposes some methods that allow making skyd API
// calls used by both the API and the blocker
type api struct {
	staticSkydHost        string
	staticSkydPort        int
	staticSkydAPIPassword string

	staticDB     *database.DB
	staticLogger *logrus.Logger
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

// BlockSkylinks will perform an API call to skyd to block the given skylinks
func (api *api) BlockHashes(hashes []string) error {
	// Build the call to skyd.
	reqBody := skyapi.SkynetBlocklistPOST{
		Add:    hashes,
		Remove: nil,
		IsHash: true,
	}
	reqBodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return errors.AddContext(err, "failed to build request body")
	}

	url := fmt.Sprintf("http://%s:%d/skynet/blocklist?timeout=%s", api.staticSkydHost, api.staticSkydPort, skydTimeout)
	api.staticLogger.Debugf("blockSkylinks: POST on %+s", url)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(reqBodyBytes))
	if err != nil {
		return errors.AddContext(err, "failed to build request to skyd")
	}
	req.Header.Set("User-Agent", "Sia-Agent")
	req.Header.Set("Authorization", api.staticAuthHeader())

	api.staticLogger.Debugf("blockSkylinks: headers: %+v", req.Header)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.AddContext(err, "failed to make request to skyd")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			api.staticLogger.Warn(errors.AddContext(err, "failed to parse response body after a failed call to skyd").Error())
			respBody = []byte{}
		}
		return errors.New(fmt.Sprintf("call to skyd failed with status '%s' and response '%s'", resp.Status, string(respBody)))
	}
	return nil
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

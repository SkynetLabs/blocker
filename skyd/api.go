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
)

const (
	// skydTimeout is the timeout of the http calls to skyd in seconds
	skydTimeout = "30"
)

// API defines the skyd API interface. It's an interface for testing purposes,
// as this allows to easily mock it and alleviates the need for a skyd instance.
type API interface {
	// BlockSkylinks adds the given skylinks to the block list.
	BlockSkylinks([]string) error
	// IsSkydUp returns true if the skyd API instance is up.
	IsSkydUp() bool
	// ResolveSkylink tries to resolve the given skylink to a V1 skylink.
	ResolveSkylink(string) (string, error)
}

// api is a helper struct that exposes some methods that allow making skyd API
// calls used by both the API and the blocker
type api struct {
	staticNginxHost string
	staticNginxPort int

	staticSkydHost        string
	staticSkydPort        int
	staticSkydAPIPassword string

	staticDB     *database.DB
	staticLogger *logrus.Logger
}

// NewAPI creates a new API instance.
func NewAPI(nginxHost string, nginxPort int, skydHost, skydPassword string, skydPort int, db *database.DB, logger *logrus.Logger) (API, error) {
	if db == nil {
		return nil, errors.New("no DB provided")
	}
	if logger == nil {
		return nil, errors.New("no logger provided")
	}

	return &api{
		staticNginxHost: nginxHost,
		staticNginxPort: nginxPort,

		staticSkydHost:        skydHost,
		staticSkydPort:        skydPort,
		staticSkydAPIPassword: skydPassword,

		staticDB:     db,
		staticLogger: logger,
	}, nil
}

// BlockSkylinks will perform an API call to skyd to block the given skylinks
func (api *api) BlockSkylinks(sls []string) error {
	// Build the call to skyd.
	reqBody := skyapi.SkynetBlocklistPOST{
		Add:    sls,
		Remove: nil,
		IsHash: false,
	}
	reqBodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return errors.AddContext(err, "failed to build request body")
	}

	url := fmt.Sprintf("http://%s:%d/skynet/blocklist?timeout=%s", api.staticNginxHost, api.staticNginxPort, skydTimeout)

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
func (api *api) ResolveSkylink(skylink string) (string, error) {
	// build the request to resolve the skylink with skyd
	url := fmt.Sprintf("http://%s:%d/skynet/resolve/%s", api.staticSkydHost, api.staticSkydPort, skylink)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return skylink, errors.AddContext(err, "failed to build request to skyd")
	}

	// set headers and execute the request
	req.Header.Set("User-Agent", "Sia-Agent")
	req.Header.Set("Authorization", api.staticAuthHeader())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return skylink, errors.AddContext(err, "failed to make request to skyd")
	}
	defer resp.Body.Close()

	// if the status code is 200 OK, extract the resolved skylink
	if resp.StatusCode == http.StatusOK {
		resolved := struct {
			Skylink string
		}{}
		err = json.NewDecoder(resp.Body).Decode(&resolved)
		if err != nil {
			return "", errors.AddContext(err, "bad response body from skyd")
		}
		return resolved.Skylink, nil
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

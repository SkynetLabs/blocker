package skyd

import (
	"bytes"
	"context"
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

// SkydAPI is a helper struct that exposes some methods that allow making skyd
// API calls used by both the API and the blocker
type SkydAPI struct {
	staticNginxHost string
	staticNginxPort int

	staticSkydHost        string
	staticSkydPort        int
	staticSkydAPIPassword string

	staticDB     *database.DB
	staticLogger *logrus.Logger
}

// NewSkydAPI creates a new Skyd API instance.
func NewSkydAPI(nginxHost string, nginxPort int, skydHost, skydPassword string, skydPort int, db *database.DB, logger *logrus.Logger) (*SkydAPI, error) {
	if db == nil {
		return nil, errors.New("no DB provided")
	}
	if logger == nil {
		return nil, errors.New("no logger provided")
	}

	return &SkydAPI{
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
func (skyd *SkydAPI) BlockSkylinks(sls []string) error {
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

	// TODO: use environment variables to specify the nginx IP and PORT
	url := fmt.Sprintf("http://%s:%d/skynet/blocklist?timeout=%s", skyd.staticNginxHost, skyd.staticNginxPort, skydTimeout)

	skyd.staticLogger.Debugf("blockSkylinks: POST on %+s", url)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(reqBodyBytes))
	if err != nil {
		return errors.AddContext(err, "failed to build request to skyd")
	}
	req.Header.Set("User-Agent", "Sia-Agent")
	req.Header.Set("Authorization", skyd.staticAuthHeader())

	skyd.staticLogger.Debugf("blockSkylinks: headers: %+v", req.Header)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.AddContext(err, "failed to make request to skyd")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			skyd.staticLogger.Warn(errors.AddContext(err, "failed to parse response body after a failed call to skyd").Error())
			respBody = []byte{}
		}
		err = errors.New(fmt.Sprintf("call to skyd failed with status '%s' and response '%s'", resp.Status, string(respBody)))
		skyd.staticLogger.Warn(err.Error())
		return err
	}
	return nil
}

// IsAllowListed will resolve the given skylink and verify it against the
// allow list, it returns true if the skylink is present on the allow list
func (skyd *SkydAPI) IsAllowListed(ctx context.Context, skylink string) bool {
	// build the request to resolve the skylink with skyd
	url := fmt.Sprintf("http://%s:%d/skynet/resolve/%s", skyd.staticSkydHost, skyd.staticSkydPort, skylink)
	skyd.staticLogger.Debugf("isAllowListed: GET on %+s", url)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		skyd.staticLogger.Error("failed to build request to skyd", err)
		return false
	}

	// set headers and execute the request
	req.Header.Set("User-Agent", "Sia-Agent")
	req.Header.Set("Authorization", skyd.staticAuthHeader())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		skyd.staticLogger.Error("failed to make request to skyd", err)
		return false
	}
	defer resp.Body.Close()

	// if the skylink was blocked it was not allow listed
	if resp.StatusCode == http.StatusUnavailableForLegalReasons {
		return false
	}

	// if the status code is 200 OK, swap the skylink against the resolved
	// skylink before checking it against the allow list
	if resp.StatusCode == http.StatusOK {
		resolved := struct {
			Skylink string
		}{}
		err = json.NewDecoder(resp.Body).Decode(&resolved)
		if err != nil {
			skyd.staticLogger.Error("bad response body from skyd", err)
			return false
		}
		skylink = resolved.Skylink
	}

	// check whether the skylink is allow listed
	allowlisted, err := skyd.staticDB.IsAllowListed(ctx, skylink)
	if err != nil {
		skyd.staticLogger.Error("failed to verify skylink against the allow list", err)
		return false
	}
	return allowlisted
}

// IsSkydUp connects to the local skyd and checks its status.
// Returns true only if skyd is fully ready.
func (skyd *SkydAPI) IsSkydUp() bool {
	status := struct {
		Ready     bool
		Consensus bool
		Gateway   bool
		Renter    bool
	}{}
	url := fmt.Sprintf("http://%s:%d/daemon/ready", skyd.staticSkydHost, skyd.staticSkydPort)
	r, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		skyd.staticLogger.Error(err)
		return false
	}
	r.Header.Set("User-Agent", "Sia-Agent")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		skyd.staticLogger.Warnf("Failed to query skyd: %s", err.Error())
		return false
	}
	defer resp.Body.Close()
	err = json.NewDecoder(resp.Body).Decode(&status)
	if err != nil {
		skyd.staticLogger.Warnf("Bad body from skyd's /daemon/ready: %s", err.Error())
		return false
	}
	return status.Ready && status.Consensus && status.Gateway && status.Renter
}

// staticAuthHeader returns the value we need to set to the `Authorization`
// header in order to call `skyd`.
func (skyd *SkydAPI) staticAuthHeader() string {
	return fmt.Sprintf("Basic %s", base64.StdEncoding.EncodeToString([]byte(":"+skyd.staticSkydAPIPassword)))
}

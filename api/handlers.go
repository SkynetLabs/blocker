package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/SkynetLabs/blocker/database"
	"github.com/julienschmidt/httprouter"
	"gitlab.com/NebulousLabs/errors"
	skyapi "gitlab.com/SkynetLabs/skyd/node/api"
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

var (
	// ErrSkylinkAllowListed is returned when we try to add a skylink to the
	// database that is part of the allow list.
	ErrSkylinkAllowListed = errors.New("skylink can not be blocked, it is part of the allow list")
)

type (
	// BlockPOST ...
	BlockPOST struct {
		Skylink  string            `json:"skylink"`
		Reporter database.Reporter `json:"reporter"`
		Tags     []string          `json:"tags"`
	}

	// statusResponse is what we return on block requests
	statusResponse struct {
		Status string `json:"status"`
	}
)

// healthGET returns the status of the service
func (api *API) healthGET(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	status := struct {
		DBAlive bool `json:"dbAlive"`
	}{}
	err := api.staticDB.Ping(r.Context())
	status.DBAlive = err == nil
	skyapi.WriteJSON(w, status)
}

// blockPOST blocks a skylink
func (api *API) blockPOST(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var body BlockPOST
	err := json.NewDecoder(r.Body).Decode(&body)
	if err != nil {
		skyapi.WriteError(w, skyapi.Error{err.Error()}, http.StatusBadRequest)
		return
	}
	body.Skylink, err = extractSkylinkHash(body.Skylink)
	if err != nil {
		skyapi.WriteError(w, skyapi.Error{errors.AddContext(err, "invalid skylink provided").Error()}, http.StatusBadRequest)
		return
	}
	// Normalise the skylink hash. We want to use the same hash encoding in the
	// database, regardless of the encoding of the skylink when we receive it -
	// base32 or base64.
	var sl skymodules.Skylink
	err = sl.LoadString(body.Skylink)
	if err != nil {
		skyapi.WriteError(w, skyapi.Error{errors.AddContext(err, "invalid skylink provided").Error()}, http.StatusBadRequest)
		return
	}

	// Check whether the skylink is on the allow list
	if api.staticIsAllowListed(r.Context(), body.Skylink) {
		skyapi.WriteError(w, skyapi.Error{ErrSkylinkAllowListed.Error()}, http.StatusBadRequest)
		return
	}

	body.Skylink = sl.String()
	skylink := &database.BlockedSkylink{
		Skylink:        body.Skylink,
		Reporter:       body.Reporter,
		Tags:           body.Tags,
		TimestampAdded: time.Now().UTC(),
	}
	// Avoid nullpointer.
	if r.Form == nil {
		r.Form = url.Values{}
	}
	sub := r.Form.Get("sub")
	if sub == "" {
		// No sub. Maybe we didn't try to fetch it? Try now. Don't log errors.
		u, err := UserFromReq(r, api.staticLogger)
		if err == nil {
			sub = u.Sub
		}
	}
	skylink.Reporter.Sub = sub
	skylink.Reporter.Unauthenticated = sub == ""
	api.staticLogger.Tracef("blockPOST will block skylink %s", skylink.Skylink)
	err = api.staticDB.BlockedSkylinkCreate(r.Context(), skylink)
	if errors.Contains(err, database.ErrSkylinkExists) {
		skyapi.WriteJSON(w, statusResponse{"duplicate"})
		return
	}
	if err != nil {
		skyapi.WriteError(w, skyapi.Error{err.Error()}, http.StatusInternalServerError)
		return
	}
	api.staticLogger.Debugf("Added skylink %s", skylink.Skylink)
	skyapi.WriteJSON(w, statusResponse{"blocked"})
}

// extractSkylinkHash extracts the skylink hash from the given skylink that
// might have protocol, path, etc. within it.
func extractSkylinkHash(skylink string) (string, error) {
	extractSkylinkRE := regexp.MustCompile("^.*([a-z0-9]{55})|([a-zA-Z0-9-_]{46}).*$")
	m := extractSkylinkRE.FindStringSubmatch(skylink)
	if len(m) < 3 || (m[1] == "" && m[2] == "") {
		return "", errors.New("no valid skylink found in string " + skylink)
	}
	if m[1] != "" {
		return m[1], nil
	}
	return m[2], nil
}

// staticIsAllowListed will resolve the given skylink and verify it against the
// allow list, it returns true if the skylink is present on the allow list
func (api *API) staticIsAllowListed(ctx context.Context, skylink string) bool {
	// build the request to resolve the skylink with skyd
	url := fmt.Sprintf("http://%s:%d/skynet/resolve/%s", SkydHost, SkydPort, skylink)
	api.staticLogger.Debugf("isAllowListed: GET on %+s", url)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		api.staticLogger.Error("failed to build request to skyd", err)
		return false
	}

	// set headers and execute the request
	req.Header.Set("User-Agent", "Sia-Agent")
	req.Header.Set("Authorization", AuthHeader())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		api.staticLogger.Error("failed to make request to skyd", err)
		return false
	}
	defer resp.Body.Close()

	// decode the body
	resolved := struct {
		Skylink string
	}{}
	err = json.NewDecoder(resp.Body).Decode(&resolved)
	if err != nil {
		api.staticLogger.Error("bad response body from skyd", err)
		return false
	}

	allowlisted, err := api.staticDB.IsAllowListed(ctx, resolved.Skylink)
	if err != nil {
		api.staticLogger.Error("failed to verify skylink against the allow list", err)
		return false
	}
	return allowlisted
}

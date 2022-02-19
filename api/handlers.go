package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"time"

	"github.com/SkynetLabs/blocker/blocker"
	"github.com/SkynetLabs/blocker/database"
	"github.com/julienschmidt/httprouter"
	"gitlab.com/NebulousLabs/errors"
	skyapi "gitlab.com/SkynetLabs/skyd/node/api"
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

var (
	// maxBodySize defines the maximum size of the POST body when making request
	// to the block endpoints
	maxBodySize = int64(1 << 16) // 64kib
)

type (
	// BlockPOST describes a request to the /block endpoint.
	BlockPOST struct {
		Skylink  skylink  `json:"skylink"`
		Reporter Reporter `json:"reporter"`
		Tags     []string `json:"tags"`
	}

	// BlockWithPoWPOST describes a request to the /blockpow endpoint
	// containing a pow.
	BlockWithPoWPOST struct {
		BlockPOST
		PoW blocker.BlockPoW `json:"pow"`
	}

	// BlockWithPoWGET is the response a user gets from the /blockpow
	// endpoint.
	BlockWithPoWGET struct {
		Target string `json:"target"`
	}

	// Reporter is a person who reported that a given skylink should be
	// blocked.
	Reporter struct {
		Name         string `json:"name"`
		Email        string `json:"email"`
		OtherContact string `json:"othercontact"`
	}

	// statusResponse is what we return on block requests
	statusResponse struct {
		Status string `json:"status"`
	}

	// skylink is a helper type which adds custom decoding for skylinks.
	skylink string
)

// UnmarshalJSON implements json.Unmarshaler for a skylink.
func (sl *skylink) UnmarshalJSON(b []byte) error {
	var link string
	err := json.Unmarshal(b, &link)
	if err != nil {
		return err
	}
	// Trim all the redundant information.
	//
	// TODO: is this really necessary? if possible we should try and drop this
	link, err = extractSkylinkHash(link)
	if err != nil {
		return err
	}
	// Normalise the skylink hash. We want to use the same hash encoding in the
	// database, regardless of the encoding of the skylink when we receive it -
	// base32 or base64.
	var slNormalized skymodules.Skylink
	err = slNormalized.LoadString(link)
	if err != nil {
		return errors.AddContext(err, "invalid skylink provided")
	}
	*sl = skylink(slNormalized.String())
	return nil
}

// healthGET returns the status of the service
func (api *API) healthGET(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	status := struct {
		DBAlive bool `json:"dbAlive"`
	}{}

	// Apply a timeout.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	err := api.staticDB.Ping(ctx)
	status.DBAlive = err == nil
	skyapi.WriteJSON(w, status)
}

// blockPOST blocks a skylink
//
// NOTE: This route requires no authentication and thus it is meant to be used
// by trusted sources such as the malware scanner or abuse email scanner. There
// is another route called 'blockWithPoWPOST' that requires some proof of work
// to be done by means of 'authenticating' the caller.
func (api *API) blockPOST(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	// Protect against large bodies.
	b := http.MaxBytesReader(w, r.Body, maxBodySize)
	defer b.Close()

	// Parse the request.
	var body BlockPOST
	err := json.NewDecoder(b).Decode(&body)
	if err != nil {
		skyapi.WriteError(w, skyapi.Error{err.Error()}, http.StatusBadRequest)
		return
	}

	// Get the sub from the form
	sub := r.FormValue("sub")
	if sub == "" {
		// No sub. Maybe we didn't try to fetch it? Try now. Don't log errors.
		u, err := UserFromReq(r, api.staticLogger)
		if err == nil {
			sub = u.Sub
		}
	}

	// Handle the request
	api.handleBlockRequest(r.Context(), w, body, sub)
}

// blockWithPoWPOST blocks a skylink. It is meant to be used by untrusted
// sources such as the abuse report skapp. The PoW prevents users from easily
// and anonymously blocking large numbers of skylinks. Instead it encourages
// reuse of proofs which improves the linkability between reports, thus allowing
// us to more easily unblock a batch of links.
func (api *API) blockWithPoWPOST(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	// Protect against large bodies.
	b := http.MaxBytesReader(w, r.Body, maxBodySize)
	defer b.Close()

	// Parse the request.
	var body BlockWithPoWPOST
	err := json.NewDecoder(b).Decode(&body)
	if err != nil {
		skyapi.WriteError(w, skyapi.Error{err.Error()}, http.StatusBadRequest)
		return
	}

	// Use the MySkyID as the sub to consider the reporter authenticated.
	sub := hex.EncodeToString(body.PoW.MySkyID[:])

	// Verify the pow.
	err = body.PoW.Verify()
	if err != nil {
		skyapi.WriteError(w, skyapi.Error{err.Error()}, http.StatusBadRequest)
		return
	}

	// Handle the request
	api.handleBlockRequest(r.Context(), w, body.BlockPOST, sub)
}

// blockWithPoWGET is the handler for the /blockpow [GET] endpoint.
func (api *API) blockWithPoWGET(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	skyapi.WriteJSON(w, BlockWithPoWGET{
		Target: hex.EncodeToString(blocker.MySkyTarget[:]),
	})
}

// handleBlockRequest is a handler that is called by both the regular and PoW
// block handlers. It executes all code which is shared between the two
// handlers.
func (api *API) handleBlockRequest(ctx context.Context, w http.ResponseWriter, bp BlockPOST, sub string) {
	// Decode the skylink, we can safely ignore the error here as LoadString
	// will have been called by the JSON decoder
	var skylink skymodules.Skylink
	_ = skylink.LoadString(string(bp.Skylink))

	// Resolve the skylink
	resolved, err := api.staticSkydAPI.ResolveSkylink(skylink)
	if err == nil {
		// replace the skylink with the resolved skylink
		skylink = resolved
	} else {
		// in case of an error we log and continue with the given skylink
		api.staticLogger.Errorf("failed to resolve skylink '%v', err: %v", skylink, err)
	}

	// Sanity check the skylink is a v1 skylink
	if !skylink.IsSkylinkV1() {
		skyapi.WriteError(w, skyapi.Error{"failed to resolve skylink"}, http.StatusInternalServerError)
		return
	}

	// Check whether the skylink is on the allow list
	if api.isAllowListed(ctx, skylink) {
		skyapi.WriteJSON(w, statusResponse{"reported"})
		return
	}

	// Block the link.
	err = api.block(ctx, bp, skylink, sub, sub == "")
	if errors.Contains(err, database.ErrSkylinkExists) {
		skyapi.WriteJSON(w, statusResponse{"duplicate"})
		return
	}
	if err != nil {
		skyapi.WriteError(w, skyapi.Error{err.Error()}, http.StatusInternalServerError)
		return
	}
	skyapi.WriteJSON(w, statusResponse{"reported"})
}

// block blocks a skylink
func (api *API) block(ctx context.Context, bp BlockPOST, skylink skymodules.Skylink, sub string, unauthenticated bool) error {
	// TODO: currently we still set the Skylink, as soon as this module is
	// converted to work fully with hashes, the Skylink field needs to be
	// dropped.
	bs := &database.BlockedSkylink{
		Skylink: skylink.String(),
		Hash:    database.NewHash(skylink),
		Reporter: database.Reporter{
			Name:            bp.Reporter.Name,
			Email:           bp.Reporter.Email,
			OtherContact:    bp.Reporter.OtherContact,
			Sub:             sub,
			Unauthenticated: unauthenticated,
		},
		Tags:           bp.Tags,
		TimestampAdded: time.Now().UTC(),
	}
	api.staticLogger.Debugf("blocking hash %s", bs.Hash)
	err := api.staticDB.CreateBlockedSkylink(ctx, bs)
	if err != nil {
		return err
	}
	api.staticLogger.Debugf("blocked hash %s", bs.Hash)
	return nil
}

// isAllowListed returns true if the given skylink is on the allow list
//
// NOTE: the given skylink is expected to be a v1 skylink, meaning the caller of
// this function should have tried to resolve the skylink beforehand
func (api *API) isAllowListed(ctx context.Context, skylink skymodules.Skylink) bool {
	allowlisted, err := api.staticDB.IsAllowListed(ctx, skylink.String())
	if err != nil {
		api.staticLogger.Error("failed to verify skylink against the allow list", err)
		return false
	}
	return allowlisted
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

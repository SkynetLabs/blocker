package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	url "net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/SkynetLabs/blocker/database"
	"github.com/SkynetLabs/blocker/modules"
	"github.com/julienschmidt/httprouter"
	"gitlab.com/NebulousLabs/errors"
	skyapi "gitlab.com/SkynetLabs/skyd/node/api"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
)

const (
	// maxBodySize defines the maximum size of the POST body when making request
	// to the block endpoints
	maxBodySize = int64(1 << 16) // 64kib

	// maxLimit defines the maximum value for the limit parameter used by the
	// blocklist endpoint
	maxLimit = 1000

	// sortAscending defines the query string parameter option that can be
	// passed as 'sort' parameter. If passed the response will contain the
	// entries sorted by the 'sortBy' parameter in ascending fashion.
	sortAscending = "asc"

	// sortDescending defines the query string parameter option that can be
	// passed as 'sort' parameter. If passed the response will contain the
	// entries sorted by the 'sortBy' parameter in descending fashion.
	sortDescending = "desc"
)

type (
	// BlockPOST describes a request to the /block endpoint.
	BlockPOST struct {
		Skylink  skylink  `json:"skylink"`
		Reporter Reporter `json:"reporter"`
		Tags     []string `json:"tags"`
	}

	// BlocklistGET returns a list of blocked hashes
	BlocklistGET struct {
		Entries []BlockedHash `json:"entries"`
		HasMore bool          `json:"hasmore"`
	}

	// BlockedHash describes a blocked hash along with the set of tags it was
	// reported with
	BlockedHash struct {
		Hash crypto.Hash `json:"hash"`
		Tags []string    `json:"tags"`
	}

	// BlockWithPoWPOST describes a request to the /blockpow endpoint
	// containing a pow.
	BlockWithPoWPOST struct {
		BlockPOST
		PoW modules.BlockPoW `json:"pow"`
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

// blocklistGET returns a list of blocked hashes and associated tags. This route
// allows paging through the result set by the following query string
// parameters: 'sort', 'offset' and 'limit', which default to 'asc', 0 and 1000.
// The results are sorted on the 'timestamp_added' field, but the caller can
// request to see the newest results first. The default limit also serves as a
// limit.
func (api *API) blocklistGET(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	// parse offset and limit parameters
	sort, offset, limit, err := parseListParameters(r.URL.Query())
	if err != nil {
		skyapi.WriteError(w, skyapi.Error{err.Error()}, http.StatusBadRequest)
		return
	}

	blocked, more, err := api.staticDB.BlockedHashes(sort, offset, limit)
	if err != nil {
		skyapi.WriteError(w, skyapi.Error{err.Error()}, http.StatusInternalServerError)
		return
	}

	hashes := make([]BlockedHash, len(blocked))
	for i, bh := range blocked {
		hashes[i] = BlockedHash{
			Hash: bh.Hash.Hash,
			Tags: bh.Tags,
		}
	}
	skyapi.WriteJSON(w, BlocklistGET{
		Entries: hashes,
		HasMore: more,
	})
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
		Target: hex.EncodeToString(modules.MySkyTarget[:]),
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
	resolved, err := api.staticSkydClient.ResolveSkylink(skylink)
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

// parseListParameters parses sort, offset and limit from the given query. If
// not present, they default to 1 ('asc'), 0 and 1000 respectively.
func parseListParameters(query url.Values) (int, int, int, error) {
	var err error

	// parse sort
	sort := 1
	sortStr := strings.ToLower(query.Get("sort"))
	if sortStr != "" {
		if !(sortStr == sortAscending || sortStr == sortDescending) {
			return 0, 0, 0, fmt.Errorf("invalid value for 'sort' parameter, can only be '%v' or '%v'", sortAscending, sortDescending)
		}
		if sortStr == sortDescending {
			sort = -1
		}
	}

	// parse offset
	var offset int
	offsetStr := query.Get("offset")
	if offsetStr != "" {
		offset, err = strconv.Atoi(offsetStr)
		if err != nil {
			return 0, 0, 0, err
		}
		if offset < 0 {
			return 0, 0, 0, fmt.Errorf("invalid value for 'offset' parameter, can not be negative")
		}
	}

	// parse limit
	limit := maxLimit
	limitStr := query.Get("limit")
	if limitStr != "" {
		limit, err = strconv.Atoi(limitStr)
		if err != nil {
			return 0, 0, 0, err
		}
		if limit < 1 || limit > maxLimit {
			return 0, 0, 0, fmt.Errorf("invalid value for 'limit' parameter, must be between 1 and %v", maxLimit)
		}
	}

	return sort, offset, limit, nil
}

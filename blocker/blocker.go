package blocker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/SkynetLabs/blocker/api"
	"github.com/SkynetLabs/blocker/database"
	"github.com/SkynetLabs/skynet-accounts/build"
	"github.com/sirupsen/logrus"
	"gitlab.com/NebulousLabs/errors"
	skyapi "gitlab.com/SkynetLabs/skyd/node/api"
)

const (
	// SkylinksChunk is the max number of skylinks to be sent for blocking
	// simultaneously.
	SkylinksChunk = 100
)

var (
	// NginxCachePurgerListPath is the path at which we can find the list where
	// we want to add the skylinks which we want purged from nginx's cache.
	//
	// NOTE: this value can be configured via the BLOCKER_NGINX_CACHE_PURGE_LIST
	// environment variable, however it is important that this path matches the
	// path in the nginx purge script that is part of the cron.
	NginxCachePurgerListPath = "/data/nginx/blocker/skylinks.txt"

	// NginxCachePurgeLockPath is the path to the lock directory. The blocker
	// acquires this lock before writing to the list file, essentially ensuring
	// the purge script does not alter the file while the blocker API is writing
	// to it.
	//
	// NOTE: this value can be configured via the BLOCKER_NGINX_CACHE_PURGE_LOCK
	// environment variable, however it is important that this path matches the
	// path in the nginx purge script that is part of the cron.
	NginxCachePurgeLockPath = "/data/nginx/blocker/lock"

	// skydTimeout is the timeout of the http calls to skyd in seconds
	skydTimeout = "30"
	// sleepBetweenScans defines how long the scanner should sleep after
	// scanning the DB and not finding any skylinks to scan.
	sleepBetweenScans = build.Select(
		build.Var{
			Dev:      10 * time.Second,
			Testing:  100 * time.Millisecond,
			Standard: time.Minute,
		},
	).(time.Duration)
	// sleepOnErrStep defines the base step for sleeping after encountering an
	// error. We'll increase the sleep by an order of magnitude on each
	// subsequent error until sleepOnErrSteps. We'll multiply that by the number
	// of consecutive errors, up to sleepOnErrSteps times.
	//
	// Example: we'll sleep for 10 secs, then 20 and so on until 60. Then we'll
	// keep sleeping for 60 seconds until the error is resolved.
	sleepOnErrStep = 10 * time.Second
	// sleepOnErrSteps is the maximum number of times we're going to increment
	// the sleep-on-error length.
	sleepOnErrSteps = 6
)

// Blocker scans the database for skylinks that should be blocked and calls
// skyd to block them.
type Blocker struct {
	staticCtx    context.Context
	staticDB     *database.DB
	staticLogger *logrus.Logger
}

// New returns a new Blocker with the given parameters.
func New(ctx context.Context, db *database.DB, logger *logrus.Logger) (*Blocker, error) {
	if ctx == nil {
		return nil, errors.New("invalid context provided")
	}
	if db == nil {
		return nil, errors.New("invalid DB provided")
	}
	if logger == nil {
		return nil, errors.New("invalid logger provided")
	}
	return &Blocker{
		staticCtx:    ctx,
		staticDB:     db,
		staticLogger: logger,
	}, nil
}

// SweepAndBlock sweeps the DB for new skylinks, blocks them in skyd and writes
// down the timestamp of the latest one, so it will scan from that moment
// further on its next sweep.
//
// Note: It actually always scans one hour before the last timestamp in order to
// avoid issues caused by clock desyncs.
func (bl Blocker) SweepAndBlock() error {
	skylinksToBlock, err := bl.staticDB.SkylinksToBlock()
	if errors.Contains(err, database.ErrNoDocumentsFound) {
		return bl.staticDB.SetLatestBlockTimestamp(time.Now().UTC())
	}
	if err != nil {
		return err
	}
	bl.staticLogger.Tracef("SweepAndBlock will block all these: %+v", skylinksToBlock)
	// Sort the skylinks in order of appearance.
	sort.Slice(skylinksToBlock, func(i, j int) bool {
		return skylinksToBlock[i].TimestampAdded.Before(skylinksToBlock[j].TimestampAdded)
	})

	// Break the list into chunks of size SkylinksChunk and block them.
	for idx := 0; idx < len(skylinksToBlock); idx += SkylinksChunk {
		end := idx + SkylinksChunk
		if end > len(skylinksToBlock) {
			end = len(skylinksToBlock)
		}
		chunk := skylinksToBlock[idx:end]
		bl.staticLogger.Tracef("SweepAndBlock will block chunk: %+v", chunk)
		block := make([]string, 0, len(chunk))
		var latestTimestamp time.Time

		for _, sl := range chunk {
			select {
			case <-bl.staticCtx.Done():
				return nil
			default:
			}

			if sl.Skylink == "" {
				bl.staticLogger.Warnf("SkylinksToBlock returned a record with an empty skylink. Record: %+v", sl)
				continue // TODO Should we `return` here?
			}
			if sl.TimestampAdded.After(latestTimestamp) {
				latestTimestamp = sl.TimestampAdded
			}
			block = append(block, sl.Skylink)
		}
		// Block the collected skylinks.
		err = bl.blockSkylinks(block)
		if err != nil && !strings.Contains(err.Error(), "no entries updated") {
			err = errors.AddContext(err, "failed to block skylinks list")
			bl.staticLogger.Tracef("SweepAndBlock failed to block with error %s", err.Error())
			return err
		}
		err = bl.staticDB.SetLatestBlockTimestamp(latestTimestamp)
		if err != nil && !strings.Contains(err.Error(), "no entries updated") {
			bl.staticLogger.Tracef("SweepAndBlock failed to update timestamp: %s", err.Error())
			return err
		}
	}

	// After we loop over all outstanding skylinks to block, we set the time of
	// the last scan to the current moment.
	err = bl.staticDB.SetLatestBlockTimestamp(time.Now().UTC())
	if err != nil && !strings.Contains(err.Error(), "no entries updated") {
		bl.staticLogger.Tracef("SweepAndBlock failed to update timestamp: %s", err.Error())
		return err
	}
	return nil
}

// Start launches a background task that periodically scans the database for
// new skylink records and sends them for blocking.
func (bl Blocker) Start() {
	// Start the blocking loop.
	go func() {
		// sleepLength defines how long the thread will sleep before scanning
		// the next skylink. Its value is controlled by SweepAndBlock - while we
		// keep finding files to scan, we'll keep this sleep at zero. Once we
		// run out of files to scan we'll reset it to its full duration of
		// sleepBetweenScans.
		var sleepLength time.Duration
		numSubsequentErrs := 0
		for {
			select {
			case <-bl.staticCtx.Done():
				return
			case <-time.After(sleepLength):
			}
			err := bl.SweepAndBlock()
			if errors.Contains(err, database.ErrNoDocumentsFound) {
				// This was a successful call, so the number of subsequent
				// errors is reset and we sleep for a pre-determined period
				// in waiting for new skylinks to be uploaded.
				sleepLength = sleepBetweenScans
				numSubsequentErrs = 0
			} else if err != nil {
				numSubsequentErrs++
				if numSubsequentErrs > sleepOnErrSteps {
					numSubsequentErrs = sleepOnErrSteps
				}
				// On error, we sleep for an increasing amount of time -
				// from 10 seconds  on the first error to 60 seconds on the
				// sixth and subsequent errors.
				sleepLength = sleepOnErrStep * time.Duration(numSubsequentErrs)
			} else {
				// A successful scan. Reset the number of subsequent errors.
				numSubsequentErrs = 0
				sleepLength = sleepBetweenScans
			}
			if err != nil {
				bl.staticLogger.Debugf("SweepAndBlock error: %s", err.Error())
			} else {
				bl.staticLogger.Debugf("SweepAndBlock ran successfully.")
			}
		}
	}()
}

// blockSkylinks calls skyd and instructs it to block the given list of
// skylinks.
func (bl *Blocker) blockSkylinks(sls []string) error {
	err := bl.writeToNginxCachePurger(sls)
	if err != nil {
		bl.staticLogger.Warnf("Failed to write to nginx cache purger's list: %s", err)
	}
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

	url := fmt.Sprintf("http://%s:%d/skynet/blocklist?timeout=%s", api.SkydHost, api.SkydPort, skydTimeout)
	bl.staticLogger.Debugf("blockSkylinks: POST on %+s", url)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(reqBodyBytes))
	if err != nil {
		return errors.AddContext(err, "failed to build request to skyd")
	}
	req.Header.Set("User-Agent", "Sia-Agent")
	req.Header.Set("Authorization", authHeader())
	bl.staticLogger.Debugf("blockSkylinks: headers: %+v", req.Header)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.AddContext(err, "failed to make request to skyd")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			bl.staticLogger.Warn(errors.AddContext(err, "failed to parse response body after a failed call to skyd").Error())
			respBody = []byte{}
		}
		err = errors.New(fmt.Sprintf("call to skyd failed with status '%s' and response '%s'", resp.Status, string(respBody)))
		bl.staticLogger.Warn(err.Error())
		return err
	}
	return nil
}

// writeToNginxCachePurger appends all given skylinks to the file at path
// NginxCachePurgerListPath from where another process will purge them from
// nginx's cache.
func (bl *Blocker) writeToNginxCachePurger(sls []string) error {
	// acquire a lock on the nginx cache list
	//
	// NOTE: we use a directory as lock file because this allows for an atomic
	// mkdir operation in the bash script that purges the skylinks in the list
	err := func() error {
		var lockErr error
		// we only attempt this 3 times with a 1s sleep in between, this should
		// not fail seeing as Nginx only moves the file
		for i := 0; i < 3; i++ {
			lockErr = os.Mkdir(NginxCachePurgeLockPath, 0700)
			if lockErr == nil {
				break
			}
			bl.staticLogger.Warnf("failed to acquire nginx lock")
			time.Sleep(time.Second)
		}
		return lockErr
	}()
	if err != nil {
		return errors.AddContext(err, "failed to acquire nginx lock")
	}

	// defer a function that releases the lock
	defer func() {
		err := os.Remove(NginxCachePurgeLockPath)
		if err != nil {
			bl.staticLogger.Errorf("failed to release nginx lock, err %v", err)
		}
	}()

	// open the nginx cache list file
	f, err := os.OpenFile(NginxCachePurgerListPath, os.O_APPEND&os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer func() {
		e1 := f.Sync()
		e2 := f.Close()
		if e1 != nil || e2 != nil {
			bl.staticLogger.Warnf("Failed to sync and close nginx cache purger list: %s", errors.Compose(e1, e2).Error())
		}
	}()
	for _, s := range sls {
		_, err = f.WriteString(s + "\n")
		if err != nil {
			return err
		}
	}
	return nil
}

// authHeader returns the value we need to set to the `Authorization` header in
// order to call `skyd`.
func authHeader() string {
	return fmt.Sprintf("Basic %s", base64.StdEncoding.EncodeToString([]byte(":"+api.SkydAPIPassword)))
}

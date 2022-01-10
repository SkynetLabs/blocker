package blocker

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/SkynetLabs/blocker/database"
	"github.com/SkynetLabs/blocker/skyd"
	"github.com/SkynetLabs/skynet-accounts/build"
	"github.com/sirupsen/logrus"
	"gitlab.com/NebulousLabs/errors"
)

const (
	// blockBatchSize is the max number of skylinks to be sent for blocking
	// simultaneously.
	blockBatchSize = 100
)

var (
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
	staticNginxCachePurgerListPath string
	staticNginxCachePurgeLockPath  string

	staticCtx     context.Context
	staticDB      *database.DB
	staticLogger  *logrus.Logger
	staticSkydAPI skyd.API
}

// New returns a new Blocker with the given parameters.
func New(ctx context.Context, skydAPI skyd.API, db *database.DB, logger *logrus.Logger, nginxCachePurgerListPath, nginxCachePurgeLockPath string) (*Blocker, error) {
	if ctx == nil {
		return nil, errors.New("invalid context provided")
	}
	if db == nil {
		return nil, errors.New("invalid DB provided")
	}
	if logger == nil {
		return nil, errors.New("invalid logger provided")
	}
	if skydAPI == nil {
		return nil, errors.New("invalid Skyd API provided")
	}
	bl := &Blocker{
		staticNginxCachePurgerListPath: nginxCachePurgerListPath,
		staticNginxCachePurgeLockPath:  nginxCachePurgeLockPath,

		staticCtx:     ctx,
		staticDB:      db,
		staticLogger:  logger,
		staticSkydAPI: skydAPI,
	}
	return bl, nil
}

// SweepAndBlock sweeps the DB for new skylinks, blocks them in skyd and writes
// down the timestamp of the latest one, so it will scan from that moment
// further on its next sweep.
//
// Note: It actually always scans one hour before the last timestamp in order to
// avoid issues caused by clock desyncs.
func (bl *Blocker) SweepAndBlock() error {
	// Fetch skylinks to block, return early if there are none
	skylinks, err := bl.staticDB.SkylinksToBlock()
	if errors.Contains(err, database.ErrNoDocumentsFound) {
		return bl.staticDB.SetLatestBlockTimestamp(time.Now().UTC())
	}
	if err != nil {
		return err
	}
	bl.staticLogger.Tracef("SweepAndBlock will block all these: %+v", skylinks)

	// Write the skylinks to the nginx purge list
	if err := bl.writeToNginxCachePurger(skylinks); err != nil {
		bl.staticLogger.Errorf("Failed to write to nginx cache purger's list: %s", err)
	}

	// Block the skylinks
	if err := bl.blockSkylinks(skylinks); err != nil {
		bl.staticLogger.Errorf("Failed to block skylinks: %s", err)
		return err
	}

	// Update the latest block timestamp
	err = bl.staticDB.SetLatestBlockTimestamp(time.Now().UTC())
	if err != nil && err != database.ErrNoEntriesUpdated {
		bl.staticLogger.Tracef("SweepAndBlock failed to update timestamp: %s", err.Error())
		return err
	}
	return nil
}

// Start launches a background task that periodically scans the database for
// new skylink records and sends them for blocking.
func (bl *Blocker) Start() {
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

// blockSkylinks blocks the given list of skylinks.
func (bl *Blocker) blockSkylinks(skylinks []database.BlockedSkylink) error {
	batchSize := blockBatchSize
	start := 0

	var failed []string
	for start < len(skylinks) {
		// check whether we need to escape
		select {
		case <-bl.staticCtx.Done():
			return nil
		default:
		}

		// calculate the end of the batch range
		end := start + batchSize
		if end > len(skylinks) {
			end = len(skylinks)
		}

		// grab all skylinks for this batch
		batch := make([]string, end-start)
		for i, sl := range skylinks[start:end] {
			batch[i] = sl.Skylink
		}

		// trace the current batch
		bl.staticLogger.Tracef("SweepAndBlock will block skylinks: %+v", batch)

		// send the batch to skyd, if an error occurs and the current batch size
		// is greater than one, we simply retry with a smaller batch size
		err := bl.staticSkydAPI.BlockSkylinks(batch)
		if err != nil && batchSize > 1 {
			bl.staticLogger.Tracef("Error occurred while blocking skylinks retrying with batch size %v, err: %v, retrying with smaller batch size...", batchSize, err)
			batchSize /= 10
			continue
		}

		// if an error occurs add it to the failed array
		if err != nil {
			failed = append(failed, fmt.Sprintf("failed blocking skylink '%v', err %v", batch[0], err))
		}

		// if no error has occurred yet, update the latest block timestamp
		if len(failed) == 0 {
			latest := skylinks[end-1]
			err = bl.staticDB.SetLatestBlockTimestamp(latest.TimestampAdded)
			if err != nil && err != database.ErrNoEntriesUpdated {
				bl.staticLogger.Tracef("blockSkylinks failed to update timestamp: %s", err.Error())
			}
		}

		// update start
		start = end
	}

	if len(failed) > 0 {
		return fmt.Errorf("block skylinks failed with errors: %v", failed)
	}
	return nil
}

// writeToNginxCachePurger appends all given skylinks to the file at path
// NginxCachePurgerListPath from where another process will purge them from
// nginx's cache.
func (bl *Blocker) writeToNginxCachePurger(skylinks []database.BlockedSkylink) error {
	// grab all skylinks as strings
	sls := make([]string, len(skylinks))
	for i, sl := range skylinks {
		sls[i] = sl.Skylink
	}

	// acquire a lock on the nginx cache list
	//
	// NOTE: we use a directory as lock file because this allows for an atomic
	// mkdir operation in the bash script that purges the skylinks in the list
	err := func() error {
		var lockErr error
		// we only attempt this 3 times with a 1s sleep in between, this should
		// not fail seeing as Nginx only moves the file
		for i := 0; i < 3; i++ {
			lockErr = os.Mkdir(bl.staticNginxCachePurgeLockPath, 0700)
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
		err := os.Remove(bl.staticNginxCachePurgeLockPath)
		if err != nil {
			bl.staticLogger.Errorf("failed to release nginx lock, err %v", err)
		}
	}()

	// open the nginx cache list file
	f, err := os.OpenFile(bl.staticNginxCachePurgerListPath, os.O_RDWR|os.O_CREATE, 0644)
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

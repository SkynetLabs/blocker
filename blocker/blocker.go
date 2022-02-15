package blocker

import (
	"context"
	"strings"
	"time"

	"github.com/SkynetLabs/blocker/database"
	"github.com/SkynetLabs/blocker/skyd"
	"github.com/SkynetLabs/skynet-accounts/build"
	"github.com/sirupsen/logrus"
	"gitlab.com/NebulousLabs/errors"
)

const (
	// blockBatchSize is the max number of (skylink) hashes to be sent for
	// blocking simultaneously.
	blockBatchSize = 100

	// blockBatchSizeDivisor is the divisor applied to the batch size when an
	// error is encountered.
	blockBatchSizeDivisor = 10

	// unableToUpdateBlocklistErrStr is a substring of the error returned by
	// skyd if the blocklist was unable to get updated
	unableToUpdateBlocklistErrStr = "unable to update the skynet blocklist"
)

var (
	// retryInterval defines the amount of time between retries of blocked
	// skylinks that failed to get blocked the first time around. This interval
	// is (a lot) higher than the interval with which we scan for skylinks to
	// get blocked.
	retryInterval = build.Select(
		build.Var{
			Dev:      time.Minute,
			Testing:  time.Second,
			Standard: 4 * time.Hour,
		},
	).(time.Duration)

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
	staticCtx     context.Context
	staticDB      *database.DB
	staticLogger  *logrus.Logger
	staticSkydAPI skyd.API
}

// New returns a new Blocker with the given parameters.
func New(ctx context.Context, skydAPI skyd.API, db *database.DB, logger *logrus.Logger) (*Blocker, error) {
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
		staticCtx:     ctx,
		staticDB:      db,
		staticLogger:  logger,
		staticSkydAPI: skydAPI,
	}
	return bl, nil
}

// BlockHashes blocks the given list of hashes.
func (bl *Blocker) BlockHashes(hashes []database.Hash) (succeeded int, failures int, err error) {
	batchSize := blockBatchSize
	start := 0

	// keep track of which skylinks were blocked and which ones failed
	var blocked []database.Hash
	var failed []database.Hash

	// defer a function that updates the database and sets return values
	defer func() {
		bErr := bl.staticDB.MarkAsSucceeded(blocked)
		fErr := bl.staticDB.MarkAsFailed(failed)

		err = errors.Compose(err, bErr, fErr)
		succeeded = len(blocked)
		failures = len(failed)
	}()

	for start < len(hashes) {
		// check whether we need to escape
		select {
		case <-bl.staticCtx.Done():
			return
		default:
		}

		// batchSize shouldn't ever be 0, but if it is zero we might get stuck
		// in an endless loop, therefor we add this check here and break to
		// ensure that never happens
		if batchSize == 0 {
			break
		}

		// calculate the end of the batch range
		end := start + batchSize
		if end > len(hashes) {
			end = len(hashes)
		}

		// grab all skylink hashes for this batch
		batch := make([]string, end-start)
		for i, hash := range hashes[start:end] {
			batch[i] = hash.String()
		}

		// send the batch to skyd, if an error occurs and the current batch size
		// is greater than one, we simply retry with a smaller batch size
		err = bl.staticSkydAPI.BlockHashes(batch)

		// if there's an error, and it's unrelated to the batch we sent, e.g.
		// connection issue or something, we return here
		if err != nil && !strings.Contains(err.Error(), unableToUpdateBlocklistErrStr) {
			return
		}

		// otherwise if there's an error and the batchsize is larger than 1, we
		// simply decrease the batch size and continue
		if err != nil && batchSize > 1 {
			bl.staticLogger.Tracef("Error occurred while blocking skylinks retrying with batch size %v, err: %v, retrying with smaller batch size...", batchSize, err)
			batchSize /= blockBatchSizeDivisor
			continue
		}

		// if an error occurs add it to the failed array
		if err != nil {
			if len(batch) == 1 {
				failed = append(failed, hashes[start])
			} else {
				bl.staticLogger.Errorf("Critical Developer Error, this code should only execute if the length of the batch equals one")
			}
		}

		// if no error occurred, add all skylinks from the batch to the
		// array of blocked skylinks
		if err == nil {
			blocked = append(blocked, hashes[start:end]...)
		}

		// update start
		start = end
	}
	return
}

// RetryFailedSkylinks fetches all blocked skylinks that failed to get blocked
// the first time and retries them.
func (bl *Blocker) RetryFailedSkylinks() error {
	// Fetch hashes to retry
	hashes, err := bl.staticDB.HashesToRetry()
	if err != nil {
		return err
	}

	// Escape early if there are none
	if len(hashes) == 0 {
		return nil
	}

	bl.staticLogger.Tracef("RetryFailedSkylinks will retry all these: %+v", hashes)

	// Retry the hashes
	blocked, failed, err := bl.BlockHashes(hashes)
	if err != nil && !strings.Contains(err.Error(), unableToUpdateBlocklistErrStr) {
		bl.staticLogger.Errorf("Failed to retry skylinks: %s", err)
		return err
	}

	bl.staticLogger.Tracef("RetryFailedSkylinks blocked %v skylinks, and had %v failures", blocked, failed)

	// NOTE: we purposefully do not update the latest block timestamp in the
	// retry loop

	return nil
}

// SweepAndBlock sweeps the DB for new skylinks, blocks them in skyd and writes
// down the timestamp of the latest one, so it will scan from that moment
// further on its next sweep.
//
// Note: It actually always scans one hour before the last timestamp in order to
// avoid issues caused by clock desyncs.
func (bl *Blocker) SweepAndBlock() error {
	// Fetch hashes to block, return early if there are none
	hashes, err := bl.staticDB.HashesToBlock()
	if err != nil {
		return err
	}

	// Escape early if there are none
	if len(hashes) == 0 {
		return bl.staticDB.SetLatestBlockTimestamp(time.Now().UTC())
	}

	bl.staticLogger.Tracef("SweepAndBlock will block all these: %+v", hashes)

	// Block the hashes
	blocked, failed, err := bl.BlockHashes(hashes)
	if err != nil {
		bl.staticLogger.Errorf("Failed to block hashes: %s", err)
		return err
	}

	bl.staticLogger.Tracef("SweepAndBlock blocked %v hashes, and had %v failures", blocked, failed)

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

	// Start the retry loop.
	go func() {
		for {
			select {
			case <-bl.staticCtx.Done():
				return
			case <-time.After(retryInterval):
			}
			err := bl.RetryFailedSkylinks()
			if err != nil {
				bl.staticLogger.Debugf("RetryFailedSkylinks error: %s", err.Error())
				continue
			}
			bl.staticLogger.Debugf("RetryFailedSkylinks ran successfully.")
		}
	}()
}

package blocker

import (
	"context"
	"sync"
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
)

var (
	// blockInterval defines the amount of time between fetching hashes that
	// need to be blocked from the database.
	blockInterval = build.Select(
		build.Var{
			Dev:      10 * time.Second,
			Testing:  100 * time.Millisecond,
			Standard: time.Minute,
		},
	).(time.Duration)

	// retryInterval defines the amount of time between retries of blocked
	// hashes that failed to get blocked the first time around. This interval
	// is (a lot) higher than the blockInterval.
	retryInterval = build.Select(
		build.Var{
			Dev:      time.Minute,
			Testing:  time.Second,
			Standard: time.Hour,
		},
	).(time.Duration)
)

type (
	// Blocker scans the database for skylinks that should be blocked and calls
	// skyd to block them.
	Blocker struct {
		started bool

		// latestBlockTime is the time at which we ran 'BlockHashes' the last
		// time, this timestamp is used as an offset when fetch all 'new' hashes
		// to block.
		latestBlockTime time.Time

		staticCtx     context.Context
		staticDB      *database.DB
		staticLogger  *logrus.Logger
		staticMu      sync.Mutex
		staticSkydAPI skyd.API
	}
)

// New returns a new Blocker with the given parameters.
func New(ctx context.Context, skydAPI skyd.API, db *database.DB, logger *logrus.Logger) (*Blocker, error) {
	if ctx == nil {
		return nil, errors.New("no context provided")
	}
	if db == nil {
		return nil, errors.New("no DB provided")
	}
	if logger == nil {
		return nil, errors.New("no logger provided")
	}
	if skydAPI == nil {
		return nil, errors.New("no Skyd API provided")
	}
	bl := &Blocker{
		staticCtx:     ctx,
		staticDB:      db,
		staticLogger:  logger,
		staticSkydAPI: skydAPI,
	}
	return bl, nil
}

// BlockHashes blocks the given list of hashes. It returns the amount of hashes
// which were blocked successfully, the amount that were invalid, and a
// potential error.
func (bl *Blocker) BlockHashes(hashes []database.Hash) (int, int, error) {
	start := 0

	// keep track of the amount of blocked and invalid hashes
	var numBlocked int
	var numInvalid int

	for start < len(hashes) {
		// check whether we need to escape
		select {
		case <-bl.staticCtx.Done():
			return numBlocked, numInvalid, nil
		default:
		}

		// calculate the end of the batch range
		end := start + blockBatchSize
		if end > len(hashes) {
			end = len(hashes)
		}

		// create the batch
		batch := hashes[start:end]

		// send the batch to skyd, if an error occurs we mark it as failed and
		// escape early because something is probably wrong
		blocked, invalid, err := bl.staticSkydAPI.BlockHashes(batch)
		if err != nil {
			err = errors.Compose(err, bl.staticDB.MarkFailed(batch))
			return numBlocked, numInvalid, err
		}

		// update the counts
		numBlocked += len(blocked)
		numInvalid += len(invalid)

		// update the documents
		err1 := bl.staticDB.MarkSucceeded(blocked)
		err2 := bl.staticDB.MarkInvalid(invalid)
		if err := errors.Compose(err1, err2); err != nil {
			return numBlocked, numInvalid, err
		}

		// update start
		start = end
	}

	return numBlocked, numInvalid, nil
}

// Start launches the two backgrounds that periodically scan for new hashes to
// block or retry hashes that failed to get blocked the first time around.
func (bl *Blocker) Start() error {
	bl.staticMu.Lock()
	defer bl.staticMu.Unlock()

	// assert 'Start' is only called once
	if bl.started {
		return errors.New("blocker already started")
	}
	bl.started = true

	// start the loops
	go bl.threadedBlockLoop()
	go bl.threadedRetryLoop()

	return nil
}

// threadedBlockLoop holds the main block loop
func (bl *Blocker) threadedBlockLoop() {
	// convenience variables
	logger := bl.staticLogger

	for {
		err := bl.managedBlock()
		if err != nil {
			logger.Debugf("threadedBlockLoop error: %v", err)
		} else {
			logger.Debugf("threadedBlockLoop ran successfully.")
		}

		select {
		case <-bl.staticCtx.Done():
			return
		case <-time.After(blockInterval):
		}
	}
}

// threadedRetryLoop holds the retry loop
func (bl *Blocker) threadedRetryLoop() {
	// convenience variables
	logger := bl.staticLogger

	for {
		err := bl.managedRetryHashes()
		if err != nil {
			logger.Debugf("threadedRetryLoop error: %v", err)
		} else {
			logger.Debugf("threadedRetryLoop ran successfully.")
		}

		select {
		case <-bl.staticCtx.Done():
			return
		case <-time.After(retryInterval):
		}
	}
}

// managedBlock sweeps the DB for new hashes to block.
func (bl *Blocker) managedBlock() error {
	now := time.Now().UTC()
	from := bl.managedLatestBlockTime()

	// Fetch hashes to block
	hashes, err := bl.staticDB.HashesToBlock(from)
	if err != nil {
		return err
	}
	if len(hashes) == 0 {
		return nil
	}

	bl.staticLogger.Tracef("SweepAndBlock will block all these: %+v", hashes)

	// Block the hashes
	blocked, invalid, err := bl.BlockHashes(hashes)
	if err != nil {
		bl.staticLogger.Errorf("Failed to block hashes: %s", err)
		return err
	}

	bl.staticLogger.Tracef("SweepAndBlock blocked %v hashes, %v invalid hashes", blocked, invalid)

	// Update the latest block time to the time immediately prior to fetching
	// the hashes from the database.
	bl.managedUpdateLatestBlockTime(now)
	return nil
}

// managedLatestBlockTime returns the latest block time
func (bl *Blocker) managedLatestBlockTime() time.Time {
	bl.staticMu.Lock()
	defer bl.staticMu.Unlock()
	return bl.latestBlockTime
}

// managedRetryHashess fetches all blocked skylinks that failed to get blocked
// the first time and retries them.
func (bl *Blocker) managedRetryHashes() error {
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
	blocked, _, err := bl.BlockHashes(hashes)
	if err != nil {
		bl.staticLogger.Errorf("Failed to retry skylinks: %s", err)
		return err
	}

	bl.staticLogger.Tracef("RetryFailedSkylinks blocked %v hashes", blocked)

	// NOTE: we purposefully do not update the latest block timestamp in the
	// retry loop

	return nil
}

// managedUpdateLatestBlockTime updates the latest block time
func (bl *Blocker) managedUpdateLatestBlockTime(latest time.Time) {
	bl.staticMu.Lock()
	defer bl.staticMu.Unlock()
	bl.latestBlockTime = latest
}

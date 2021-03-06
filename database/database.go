package database

import (
	"context"
	"fmt"
	"io/ioutil"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"gitlab.com/NebulousLabs/errors"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
	"go.sia.tech/siad/crypto"
)

const (
	// MongoDefaultTimeout is the timeout for the context used in testing
	// whenever a context is sent to mongo
	MongoDefaultTimeout = time.Minute

	// mongoIndexCreateTimeout is the timeout used when creating indices
	mongoIndexCreateTimeout = time.Minute

	// mongoTestUsername is the username used for the test database.
	mongoTestUsername = "admin"

	// mongoTestPassword is the password used for the test database.
	mongoTestPassword = "aO4tV5tC1oU3oQ7u" // #nosec G101

	// mongoTestConnString is the connection string used for the test database.
	mongoTestConnString = "mongodb://localhost:37017"
)

var (
	// ErrDuplicateKey is returned when an insert is attempted that violates the
	// unique constraint on a certain field.
	ErrDuplicateKey = errors.New("E11000 duplicate key")

	// ErrIndexCreateFailed is returned when an error occurred when trying to
	// ensure an index
	ErrIndexCreateFailed = errors.New("failed to create index")

	// ErrIndexDropFailed is returned when an error occurred when trying to
	// drop an index
	ErrIndexDropFailed = errors.New("failed to drop an index")

	// ErrNoDocumentsFound is returned when a database operation completes
	// successfully but it doesn't find or affect any documents.
	ErrNoDocumentsFound = errors.New("no documents")

	// ErrNoEntriesUpdated is returned when no entries were updated after an
	// update was performed.
	ErrNoEntriesUpdated = errors.New("no entries updated")

	// ErrSkylinkExists is returned when we try to add a skylink to the database
	// and it already exists there.
	ErrSkylinkExists = errors.New("skylink already exists")

	// ServerUID is a random string that uniquely identifies the server
	ServerUID string

	// True is a helper value, so we can pass a *bool to MongoDB's methods.
	True = true

	// dbName defines the name of the database this service uses
	dbName = "blocker"

	// collSkylinks defines the name of the skylinks collection
	collSkylinks = "skylinks"

	// collAllowlist defines the name of the allowlist collection
	collAllowlist = "allowlist"
)

// DB holds a connection to the database, as well as helpful shortcuts to
// collections and utilities.
//
// NOTE: update the 'Purge' method when adding new collections
type DB struct {
	staticClient    *mongo.Client
	staticDB        *mongo.Database
	staticAllowList *mongo.Collection
	staticSkylinks  *mongo.Collection
	staticLogger    *logrus.Logger
}

// New creates a new database connection.
func New(ctx context.Context, uri string, creds options.Credential, logger *logrus.Logger) (*DB, error) {
	return NewCustomDB(ctx, uri, dbName, creds, logger)
}

// NewCustomDB creates a new database connection to a database with a custom
// name.
func NewCustomDB(ctx context.Context, uri string, dbName string, creds options.Credential, logger *logrus.Logger) (*DB, error) {
	if ctx == nil {
		return nil, errors.New("no context provided")
	}
	if logger == nil {
		return nil, errors.New("no logger provided")
	}

	// Prepare the options for connecting to the db.
	opts := options.Client().
		ApplyURI(uri).
		SetAuth(creds).
		SetReadPreference(readpref.Primary()).
		SetWriteConcern(writeconcern.New(
			writeconcern.WMajority(),
			writeconcern.WTimeout(time.Second*30),
		)).
		SetCompressors([]string{"zstd,zlib,snappy"})

	c, err := mongo.NewClient(opts)
	if err != nil {
		return nil, errors.AddContext(err, "failed to create a new db client")
	}
	err = c.Connect(ctx)
	if err != nil {
		return nil, errors.AddContext(err, "failed to connect to db")
	}

	// Ensure the database schema
	db := c.Database(dbName)
	err = ensureDBSchema(ctx, db, logger)
	if err != nil && errors.Contains(err, ErrIndexCreateFailed) {
		// We do not error out if we failed to ensure the existence of an index.
		// It is definitely an issue that should be looked into, which is why we
		// tag it as [CRITICAL], but seeing as the blocker will work the same
		// without the index it's no reason to prevent it from running.
		logger.Errorf(`[CRITICAL] failed to ensure DB schema, err: %v`, err)
	} else if err != nil {
		return nil, err
	}

	// Define the database
	cdb := &DB{
		staticClient:    c,
		staticDB:        db,
		staticAllowList: db.Collection(collAllowlist),
		staticSkylinks:  db.Collection(collSkylinks),
		staticLogger:    logger,
	}

	return cdb, nil
}

// NewTestDB returns a test database, the database gets purged and on error we
// panic.
func NewTestDB(ctx context.Context, dbName string) *DB {
	// create a discard logger
	logger := logrus.New()
	logger.Out = ioutil.Discard

	// create the database, we swap out slashes for underscores so callers can
	// easily pass t.Name()
	dbName = strings.Replace(dbName, "/", "_", -1)
	db, err := NewCustomDB(ctx, mongoTestConnString, dbName, options.Credential{
		Username: mongoTestUsername,
		Password: mongoTestPassword,
	}, logger)
	if err != nil {
		panic(err)
	}

	// create a context with timeout and purge the database
	ctx, cancel := context.WithTimeout(ctx, MongoDefaultTimeout)
	defer cancel()
	err = db.Purge(ctx)
	if err != nil {
		panic(err)
	}

	return db
}

// BlockedHashes allows to pass a skip and limit parameter and returns an array
// of blocked hashes alongside a boolean that indicates whether there's more
// documents after the current 'page'.
func (db *DB) BlockedHashes(ctx context.Context, sort, skip, limit int) ([]BlockedSkylink, bool, error) {
	// configure the options
	opts := options.Find()
	opts.SetSkip(int64(skip))
	opts.SetLimit(int64(limit + 1))
	opts.SetSort(bson.M{"timestamp_added": sort})

	// fetch the documents
	docs, err := db.find(ctx, bson.M{
		"invalid": bson.M{"$ne": true},
		"hash":    bson.M{"$exists": true},
	}, opts)
	if err != nil {
		return nil, false, err
	}

	// we have done the find with "limit+1" because that allows us to return
	// whether there are "more" documents after the given offset, we however do
	// not want to return this document, but instead return 'true' if it existed
	if len(docs) > int(limit) {
		return docs[:limit], true, nil
	}
	return docs, false, nil
}

// Close disconnects the db.
func (db *DB) Close(ctx context.Context) error {
	return db.staticClient.Disconnect(ctx)
}

// CreateBlockedSkylink creates a new skylink. If the skylink already exists it
// returns ErrSkylinkExists.
func (db *DB) CreateBlockedSkylink(ctx context.Context, skylink *BlockedSkylink) error {
	// Ensure the given object has all required properties set
	err := skylink.Validate()
	if err != nil {
		return errors.AddContext(err, "unexpected blocked skylink")
	}

	// Insert the skylink
	_, err = db.staticSkylinks.InsertOne(ctx, skylink)
	if isDuplicateKey(err) {
		return ErrSkylinkExists
	}
	if err != nil {
		db.staticLogger.Debugf("CreateBlockedSkylink: mongodb error '%v'", err)
		return err
	}
	return nil
}

// CreateBlockedSkylinkBulk creates new blocked skylinks in bulk. It returns the
// number of created entries.
func (db *DB) CreateBlockedSkylinkBulk(ctx context.Context, skylinks []BlockedSkylink) (int, error) {
	// Convenience variables
	logger := db.staticLogger

	// Ensure all required properties are set on the given blocked skylinks
	for _, skylink := range skylinks {
		err := skylink.Validate()
		if err != nil {
			return 0, errors.AddContext(err, "unexpected blocked skylink")
		}
	}

	// Convert the given array to an interface array
	docs := make([]interface{}, len(skylinks))
	for i, doc := range skylinks {
		docs[i] = doc
	}

	// Create insert options, we set ordered to false to ensure a single write
	// failure doesn't prevent the other writes from going through. We need this
	// because we expect duplicates and want to simply ignore them.
	opts := options.InsertMany()
	opts.SetOrdered(false)

	// Insert all objects in the database
	res, err := db.staticSkylinks.InsertMany(ctx, docs, opts)

	// Handle the error, we want to ignore all duplicate key errors
	err = ignoreDuplicateKeyErrors(err)
	if err != nil {
		logger.Debugf("CreateBlockedSkylinkBulk: mongodb error '%v'", err)
		return 0, err
	}

	return len(res.InsertedIDs), nil
}

// CreateAllowListedSkylink creates a new allowlisted skylink. If the skylink
// already exists it does nothing and returns without failure.
func (db *DB) CreateAllowListedSkylink(ctx context.Context, skylink *AllowListedSkylink) error {
	// insert the skylink
	_, err := db.staticAllowList.InsertOne(ctx, skylink)
	if err != nil && !isDuplicateKey(err) {
		return err
	}
	return nil
}

// FindByHash fetches the DB record that corresponds to the given hash
// from the database.
func (db *DB) FindByHash(ctx context.Context, hash Hash) (*BlockedSkylink, error) {
	return db.findOne(ctx, bson.M{"hash": hash.String()})
}

// IsAllowListed returns whether the given skylink is on the allow list.
func (db *DB) IsAllowListed(ctx context.Context, hash crypto.Hash) (bool, error) {
	res := db.staticAllowList.FindOne(ctx, bson.M{"hash": hash.String()})
	if isDocumentNotFound(res.Err()) {
		return false, nil
	}
	if res.Err() != nil {
		return false, res.Err()
	}
	return true, nil
}

// MarkFailed will mark the given documents as failed
func (db *DB) MarkFailed(ctx context.Context, hashes []Hash) error {
	return db.updateFailedFlag(ctx, hashes, true)
}

// MarkInvalid will mark the given documents as invalid
func (db *DB) MarkInvalid(ctx context.Context, hashes []Hash) error {
	// return early if no hashes were given
	if len(hashes) == 0 {
		return nil
	}

	// create the filter
	filter := bson.M{
		"hash": bson.M{"$in": hashes},
	}

	// define the update
	update := bson.M{
		"$set": bson.M{
			"invalid": True,
		},
	}

	// perform the update
	collSkylinks := db.staticDB.Collection(collSkylinks)
	_, err := collSkylinks.UpdateMany(ctx, filter, update)
	return err
}

// MarkSucceeded will toggle the failed flag for all documents in the given
// list of hashes that are currently marked as failed.
func (db *DB) MarkSucceeded(ctx context.Context, hashes []Hash) error {
	return db.updateFailedFlag(ctx, hashes, false)
}

// Ping sends a ping command to verify that the client can connect to the DB and
// specifically to the primary.
func (db *DB) Ping(ctx context.Context) error {
	return db.staticDB.Client().Ping(ctx, readpref.Primary())
}

// Purge deletes all documents from all collections in the database
//
// NOTE: this function should never be called in production and should only be
// used for testing purposes
func (db *DB) Purge(ctx context.Context) error {
	_, err := db.staticSkylinks.DeleteMany(ctx, bson.D{})
	if err != nil {
		return errors.AddContext(err, "failed to purge skylinks collection")
	}
	_, err = db.staticAllowList.DeleteMany(ctx, bson.D{})
	if err != nil {
		return errors.AddContext(err, "failed to purge allowlist collection")
	}
	return nil
}

// HashesToBlock sweeps the database for unblocked hashes after the given
// timestamp.
func (db *DB) HashesToBlock(ctx context.Context, from time.Time) ([]Hash, error) {
	// NOTE: $ne: true is not the same as $eq: false
	filter := bson.M{
		"timestamp_added": bson.M{"$gte": from},
		"failed":          bson.M{"$ne": true},
		"invalid":         bson.M{"$ne": true},
	}
	opts := options.Find()
	opts.SetProjection(bson.M{"hash": 1})

	docs, err := db.find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}

	// Extract the hashes
	hashes := make([]Hash, len(docs))
	for i, doc := range docs {
		hashes[i] = doc.Hash
	}
	return hashes, nil
}

// HashesToRetry returns all hashes that failed to get blocked the first time
// around. This is a retry mechanism to ensure we keep retrying to block those
// hashes, but at the same try 'unblock' the main block loop in order for it
// to run smoothly.
func (db *DB) HashesToRetry(ctx context.Context) ([]Hash, error) {
	// NOTE: $ne: true is not the same as $eq: false
	filter := bson.M{
		"failed":  bson.M{"$eq": true},
		"invalid": bson.M{"$ne": true},
	}
	opts := options.Find()
	opts.SetProjection(bson.M{"hash": 1})

	docs, err := db.find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}

	// Extract the hashes
	hashes := make([]Hash, len(docs))
	for i, doc := range docs {
		hashes[i] = doc.Hash
	}
	return hashes, nil
}

// find wraps the `Find` function on the Skylinks collection and returns an
// array of decoded blocked skylink objects
func (db *DB) find(ctx context.Context, filter interface{},
	opts ...*options.FindOptions) ([]BlockedSkylink, error) {
	c, err := db.staticDB.Collection(collSkylinks).Find(ctx, filter, opts...)
	if isDocumentNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	list := make([]BlockedSkylink, 0)
	err = c.All(ctx, &list)
	if err != nil {
		return nil, err
	}
	return list, nil
}

// findOne wraps the `FindOne` function on the Skylinks collection and returns
// a decoded blocked skylink object
func (db *DB) findOne(ctx context.Context, filter interface{},
	opts ...*options.FindOneOptions) (*BlockedSkylink, error) {
	sr := db.staticDB.Collection(collSkylinks).FindOne(ctx, filter, opts...)
	if isDocumentNotFound(sr.Err()) {
		return nil, nil
	}
	if sr.Err() != nil {
		return nil, sr.Err()
	}

	var sl BlockedSkylink
	err := sr.Decode(&sl)
	if err != nil {
		return nil, err
	}
	return &sl, nil
}

// updateFailedFlag is a helper method that updates the failed flag on the
// documents that correspond with the skylinks in the given array.
func (db *DB) updateFailedFlag(ctx context.Context, hashes []Hash, failed bool) error {
	// return early if no hashes were given
	if len(hashes) == 0 {
		return nil
	}

	// create the filter, make sure to specify currently unblocked skylinks
	filter := bson.M{
		"hash":   bson.M{"$in": hashes},
		"failed": bson.M{"$eq": !failed},

		// just to be on the safe side we ensure we never update invalid
		// documents, the filters that fetch documents do this as well so this
		// is only here to keep the database as clean as possible
		"invalid": bson.M{"$eq": false},
	}

	// define the update
	update := bson.M{
		"$set": bson.M{
			"failed": failed,
		},
	}

	// perform the update
	collSkylinks := db.staticDB.Collection(collSkylinks)
	_, err := collSkylinks.UpdateMany(ctx, filter, update)
	return err
}

// ignoreDuplicateKeyErrors takes an error, if that error is a mongo
// BulkWriteException, it will loop through the write errors and ignore
// duplicate key errors. If all write errors were duplicate key errors, this
// function returns nil, otherwise it simply returns the given error.
func ignoreDuplicateKeyErrors(err error) error {
	if err == nil {
		return nil
	}

	// check whether the given error is a BulkWriteException, if it's not simply
	// return the error
	bWriteErr, ok := err.(mongo.BulkWriteException)
	if !ok {
		return err
	}

	// loop over all write errors, ignore the duplicate key errors, if all write
	// errors are duplicate key errors we want to ignore the bulk write error
	// all together
	for _, bWriteError := range bWriteErr.WriteErrors {
		if !isDuplicateKey(bWriteError) {
			return err
		}
	}
	return nil
}

// ensureDBSchema checks that we have all collections and indexes we need and
// creates them if needed.
// See https://docs.mongodb.com/manual/indexes/
// See https://docs.mongodb.com/manual/core/index-unique/
func ensureDBSchema(ctx context.Context, db *mongo.Database, log *logrus.Logger) error {
	// schema defines a mapping between a collection name and the indexes that
	// must exist for that collection.
	schema := map[string][]mongo.IndexModel{
		collAllowlist: {
			{
				Keys:    bson.M{"hash": 1},
				Options: options.Index().SetName("hash").SetUnique(true),
			},
			{
				Keys:    bson.M{"timestamp_added": 1},
				Options: options.Index().SetName("timestamp_added"),
			},
		},
		collSkylinks: {
			{
				Keys:    bson.M{"hash": 1},
				Options: options.Index().SetName("hash").SetUnique(true),
			},
			{
				Keys:    bson.M{"timestamp_added": 1},
				Options: options.Index().SetName("timestamp_added"),
			},
			{
				Keys:    bson.M{"failed": 1},
				Options: options.Index().SetName("failed"),
			},
			{
				Keys:    bson.M{"invalid": 1},
				Options: options.Index().SetName("invalid"),
			},
		},
	}

	// build the options
	opts := options.CreateIndexes()
	opts.SetMaxTime(mongoIndexCreateTimeout)
	opts.SetCommitQuorumString("majority") // defaults to all

	// ensure all collections and indices exist
	var createErr error
	for collName, models := range schema {
		coll, err := ensureCollection(ctx, db, collName)
		if err != nil {
			// no need to continue if ensuring a collection fails
			return err
		}

		iv := coll.Indexes()
		names, err := iv.CreateMany(ctx, models, opts)
		if err != nil {
			// if the index creation fails, compose the error but continue to
			// try and ensure the rest of the database schema
			createErr = errors.Compose(createErr, errors.AddContext(err, fmt.Sprintf("collection '%v'", collName)))
			continue
		}

		log.Debugf("Ensured index exists: %v | %v", collName, names)
	}
	if createErr != nil {
		createErr = errors.Compose(createErr, ErrIndexCreateFailed)
	}

	// drop the old indices on 'skylink'
	_, err1 := dropIndex(ctx, db.Collection(collAllowlist), "skylink")
	_, err2 := dropIndex(ctx, db.Collection(collSkylinks), "skylink")
	dropErr := errors.Compose(err1, err2)
	if dropErr != nil {
		dropErr = errors.Compose(dropErr, ErrIndexDropFailed)
	}

	return errors.Compose(createErr, dropErr)
}

// dropIndex is a helper function that drops the index with given name on the
// given collection
func dropIndex(ctx context.Context, coll *mongo.Collection, indexName string) (bool, error) {
	hasIndex, err := hasIndex(ctx, coll, indexName)
	if err != nil {
		return false, err
	}

	if !hasIndex {
		return false, nil
	}

	_, err = coll.Indexes().DropOne(ctx, indexName)
	if err != nil {
		return false, err
	}

	return true, nil
}

// hasIndex is a helper function that returns true if the given collection has
// an index with given name
func hasIndex(ctx context.Context, coll *mongo.Collection, indexName string) (bool, error) {
	idxs := coll.Indexes()

	cur, err := idxs.List(ctx)
	if err != nil {
		return false, err
	}

	var result []bson.M
	err = cur.All(ctx, &result)
	if err != nil {
		return false, err
	}

	found := false
	for _, v := range result {
		for k, v1 := range v {
			if k == "name" && v1 == indexName {
				found = true
			}
		}
	}
	return found, nil
}

// ensureCollection gets the given collection from the
// database and creates it if it doesn't exist.
func ensureCollection(ctx context.Context, db *mongo.Database, collName string) (*mongo.Collection, error) {
	coll := db.Collection(collName)
	if coll == nil {
		err := db.CreateCollection(ctx, collName)
		if err != nil {
			return nil, err
		}
		coll = db.Collection(collName)
		if coll == nil {
			return nil, errors.New("failed to create collection " + collName)
		}
	}
	return coll, nil
}

// isDocumentNotFound is a helper function that returns whether the given error
// contains the mongo documents not found error message.
func isDocumentNotFound(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), ErrNoDocumentsFound.Error())
}

// isDuplicateKey is a helper function that returns whether the given error
// contains the mongo duplicate key error message.
func isDuplicateKey(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), ErrDuplicateKey.Error())
}

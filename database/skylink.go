package database

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// BlockedSkylink is a skylink blocked by an external request.
type BlockedSkylink struct {
	ID                primitive.ObjectID `bson:"_id,omitempty" json:"-"`
	Skylink           string             `bson:"skylink" json:"skylink"`
	Reporter          Reporter           `bson:"reporter" json:"reporter"`
	Reverted          bool               `bson:"reverted" json:"reverted"`
	RevertedTags      []string           `bson:"reverted_tags" json:"revertedTags"`
	Tags              []string           `bson:"tags" json:"tags"`
	TimestampAdded    time.Time          `bson:"timestamp_added" json:"timestampAdded"`
	TimestampReverted time.Time          `bson:"timestamp_reverted" json:"timestampReverted"`
}

// Reporter is a person who reported that a given skylink should be blocked.
type Reporter struct {
	Name         string `bson:"name" json:"name"`
	Email        string `bson:"email" json:"email"`
	OtherContact string `bson:"other_contact" json:"otherContact"`
	Sub          string `bson:"sub,omitempty" json:"sub,omitempty"`
}

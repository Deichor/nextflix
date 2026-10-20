package segments

import "time"

// SkipSegment caches the community lookup of one episode for one file length (seconds).
// Times are seconds; 0 means "none" (a credits end of 0 means up to the end of the file).
type SkipSegment struct {
	ID           uint   `gorm:"primaryKey"`
	TmdbID       int    `gorm:"index:idx_skip_ep"`
	Season       int    `gorm:"index:idx_skip_ep"`
	Episode      int    `gorm:"index:idx_skip_ep"`
	Duration     int    // file length the lookup was for, rounded by lookupLength (TheIntroDB picks the release version by it)
	Source       string // theintrodb, introdb; "" when neither knew the episode
	IntroStart   float64
	IntroEnd     float64
	CreditsStart float64
	CreditsEnd   float64
	Incomplete   bool      // a segment is missing because a database failed (asked again after missingTTL)
	FetchedAt    time.Time `gorm:"index"`
}

func (SkipSegment) TableName() string { return "skip_segments" }

func Models() []any { return []any{&SkipSegment{}} }

// Segment is a part of the episode the player offers to skip. End is where it ends (the file's
// length for credits that run to the end).
type Segment struct {
	Start  float64 `json:"start"`
	End    float64 `json:"end"`
	Source string  `json:"source"` // chapters, theintrodb or introdb
}

// Result is what GET /api/skip-segments answers; a missing segment is null.
type Result struct {
	Intro    *Segment `json:"intro"`
	Credits  *Segment `json:"credits"`
	Duration float64  `json:"duration"`
}

// Chapter is a chapter marker of the playing file, in seconds.
type Chapter struct {
	Start, End float64
	Title      string
}

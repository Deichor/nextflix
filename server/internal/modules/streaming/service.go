package streaming

import (
	"strings"
	"sync"

	"gorm.io/gorm"
)

type Config struct {
	TorrServerURL string
	JackettURL    string
	JackettAPIKey string
}

type Service struct {
	db  *gorm.DB
	cfg Config

	probeCache   sync.Map // "hash:index" -> *tsProbe
	resolveCache sync.Map // resolve query key -> tsCacheEntry
	jackettBase  sync.Map // "base" -> last Jackett URL that answered
}

func NewService(db *gorm.DB, cfg Config) *Service {
	return &Service{db: db, cfg: cfg}
}

func (s *Service) torrServerURL() string {
	if u := strings.TrimSpace(s.cfg.TorrServerURL); u != "" {
		return strings.TrimSuffix(u, "/")
	}
	return "http://torrserver:8090"
}

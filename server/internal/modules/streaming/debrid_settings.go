package streaming

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"nextflix/internal/platform/torbox"
)

// DebridSettings holds the TorBox account (single row). The key is write-only: the API only
// answers whether one is set. Booleans default to false in the table, so the ones that are
// on by default are stored negated.
type DebridSettings struct {
	ID           uint   `gorm:"primaryKey"`
	TorBoxAPIKey string `gorm:"column:torbox_api_key"`
	// TorBox is used once a key is set, unless this is set
	TorBoxDisabled bool `gorm:"column:torbox_disabled"`
	PreferDisabled bool // cached releases rank first in their resolution, unless this is set
	AddUncached    bool // the picked torrent source is also added to TorBox (it downloads it there)
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (DebridSettings) TableName() string { return "debrid_settings" }

type DebridSettingsView struct {
	TorBox struct {
		APIKeySet   bool `json:"api_key_set"`
		Enabled     bool `json:"enabled"`
		Prefer      bool `json:"prefer"`
		AddUncached bool `json:"add_uncached"`
	} `json:"torbox"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (st *DebridSettings) View() DebridSettingsView {
	var v DebridSettingsView
	v.TorBox.APIKeySet = st.TorBoxAPIKey != ""
	v.TorBox.Enabled = !st.TorBoxDisabled
	v.TorBox.Prefer = !st.PreferDisabled
	v.TorBox.AddUncached = st.AddUncached
	v.UpdatedAt = st.UpdatedAt
	return v
}

// DebridSettingsInput is the body of PUT /api/settings/debrid. For api_key a missing field
// or an empty string keeps the stored key and null clears it.
type DebridSettingsInput struct {
	TorBox *struct {
		APIKey      optString `json:"api_key"`
		Enabled     *bool     `json:"enabled"`
		Prefer      *bool     `json:"prefer"`
		AddUncached *bool     `json:"add_uncached"`
	} `json:"torbox"`
}

func loadDebridSettings(db *gorm.DB) (*DebridSettings, error) {
	var rows []DebridSettings
	if err := db.Order("id").Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return &DebridSettings{}, nil
	}
	return &rows[0], nil
}

func (s *Service) DebridSettings() (*DebridSettings, error) {
	if s.db == nil {
		return &DebridSettings{}, nil
	}
	return loadDebridSettings(s.db)
}

func (s *Service) UpdateDebridSettings(in DebridSettingsInput) (*DebridSettings, error) {
	if s.db == nil {
		return nil, errors.New("no database")
	}
	st, err := s.DebridSettings()
	if err != nil {
		return nil, err
	}
	if tb := in.TorBox; tb != nil {
		switch k := tb.APIKey; {
		case k.Null:
			st.TorBoxAPIKey = ""
		case strings.TrimSpace(k.Value) != "":
			v := strings.TrimSpace(k.Value)
			if len(v) > 200 || strings.ContainsAny(v, " \t\r\n") {
				return nil, SettingsError{"Invalid API key"}
			}
			st.TorBoxAPIKey = v
		}
		if tb.Enabled != nil {
			st.TorBoxDisabled = !*tb.Enabled
		}
		if tb.Prefer != nil {
			st.PreferDisabled = !*tb.Prefer
		}
		if tb.AddUncached != nil {
			st.AddUncached = *tb.AddUncached
		}
	}
	if st.ID == 0 {
		err = s.db.Create(st).Error
	} else {
		err = s.db.Model(st).Select("torbox_api_key", "torbox_disabled", "prefer_disabled", "add_uncached", "updated_at").
			Updates(st).Error
	}
	if err != nil {
		return nil, err
	}
	s.debrid.settingsChanged()
	return st, nil
}

var errDebridNoKey = errors.New("TorBox API key is not set")

// TestDebrid calls TorBox's user/me with key (the stored key when empty).
func (s *Service) TestDebrid(ctx context.Context, key string) (*torbox.User, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		st, err := s.DebridSettings()
		if err != nil {
			return nil, err
		}
		key = st.TorBoxAPIKey
	}
	if key == "" {
		return nil, errDebridNoKey
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return torbox.New(s.debrid.base, key, s.debrid.hc).Me(ctx)
}

func (h handler) getDebridSettings(c *gin.Context) {
	st, err := h.s.DebridSettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load debrid settings"})
		return
	}
	c.JSON(http.StatusOK, st.View())
}

func (h handler) updateDebridSettings(c *gin.Context) {
	var in DebridSettingsInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	st, err := h.s.UpdateDebridSettings(in)
	var serr SettingsError
	switch {
	case errors.As(err, &serr):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update debrid settings"})
	default:
		c.JSON(http.StatusOK, st.View())
	}
}

func (h handler) testDebridSettings(c *gin.Context) {
	var in struct {
		TorBox struct {
			APIKey string `json:"api_key"`
		} `json:"torbox"`
	}
	_ = c.ShouldBindJSON(&in) // the body is optional
	u, err := h.s.TestDebrid(c.Request.Context(), in.TorBox.APIKey)
	var apiErr *torbox.APIError
	switch {
	case errors.Is(err, errDebridNoKey):
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
	case errors.As(err, &apiErr) && apiErr.Unauthorized():
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "error": "TorBox rejected the API key"})
	case err != nil:
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "error": "TorBox is not reachable: " + err.Error()})
	default:
		expires := any(nil)
		if u.PremiumExpiresAt != "" {
			expires = u.PremiumExpiresAt
		}
		active := u.Plan > 0
		if t, perr := time.Parse(time.RFC3339, u.PremiumExpiresAt); perr == nil {
			expires = t.UTC().Format(time.RFC3339)
			active = active && t.After(time.Now())
		}
		c.JSON(http.StatusOK, gin.H{
			"success":            true,
			"plan":               u.Plan,
			"plan_name":          torbox.PlanName(u.Plan),
			"premium_expires_at": expires,
			"active":             active,
		})
	}
}

package profiles

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"
)

// ProfileDataCleaner is implemented by modules that keep per-profile data.
type ProfileDataCleaner interface {
	DeleteByProfile(tx *gorm.DB, profileID uint) error
}

// ProfileDeletedHook is optionally implemented by cleaners that must act only after the deletion committed.
type ProfileDeletedHook interface {
	ProfileDeleted(profileID uint)
}

type Service struct {
	db       *gorm.DB
	cleaners []ProfileDataCleaner
}

func NewService(db *gorm.DB, cleaners ...ProfileDataCleaner) *Service {
	return &Service{db: db, cleaners: cleaners}
}

var (
	ErrNotFound    = errors.New("profile not found")
	ErrLimit       = errors.New("You can have at most 5 profiles")
	ErrLastProfile = errors.New("The last profile cannot be deleted")

	profileColorPattern = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)
)

// ValidationError carries a message that is safe to show to the client.
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

// Input holds the optional profile fields of a create/update request.
type Input struct {
	Name     *string `json:"name"`
	Avatar   *string `json:"avatar"`
	Color    *string `json:"color"`
	Language *string `json:"language"`
}

// applyTo validates the given fields and copies them onto p
func (in Input) applyTo(p *Profile) error {
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return ValidationError{"Name is required"}
		}
		if utf8.RuneCountInString(name) > maxProfileNameLen {
			return ValidationError{"Name must be at most 20 characters"}
		}
		p.Name = name
	}
	if in.Avatar != nil {
		avatar := strings.TrimSpace(*in.Avatar)
		if len(avatar) > 32 {
			return ValidationError{"Invalid avatar"}
		}
		if avatar == "" {
			avatar = defaultProfileAvatar
		}
		p.Avatar = avatar
	}
	if in.Color != nil {
		color := strings.TrimSpace(*in.Color)
		if color == "" {
			color = defaultProfileColor
		}
		if !profileColorPattern.MatchString(color) {
			return ValidationError{"Color must be a hex value like #E50914"}
		}
		p.Color = color
	}
	if in.Language != nil {
		lang := strings.ToLower(strings.TrimSpace(*in.Language))
		if lang == "" {
			lang = defaultLanguage
		}
		if !supportedLanguages[lang] {
			return ValidationError{"Language must be en or tr"}
		}
		p.Language = lang
	}
	return nil
}

// EnsureDefault seeds the "Home" profile when none exist.
func (s *Service) EnsureDefault() error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		n, err := countProfiles(tx)
		if err != nil || n > 0 {
			return err
		}
		return tx.Create(&Profile{Name: defaultProfileName, Avatar: defaultProfileAvatar, Color: defaultProfileColor, Language: defaultLanguage}).Error
	})
}

// Exists reports whether a profile with id exists.
func (s *Service) Exists(id uint) (bool, error) {
	var n int64
	err := s.db.Model(&Profile{}).Where("id = ?", id).Count(&n).Error
	return n > 0, err
}

func (s *Service) List() ([]Profile, error) {
	return listProfiles(s.db)
}

func (s *Service) Get(id uint) (*Profile, error) {
	p, err := findProfile(s.db, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return p, err
}

func (s *Service) Create(in Input) (*Profile, error) {
	if in.Name == nil {
		return nil, ValidationError{"Name is required"}
	}
	p := Profile{Avatar: defaultProfileAvatar, Color: defaultProfileColor, Language: defaultLanguage}
	if err := in.applyTo(&p); err != nil {
		return nil, err
	}
	// Count and insert in one (immediate) transaction so concurrent creates cannot pass the limit
	err := s.db.Transaction(func(tx *gorm.DB) error {
		n, err := countProfiles(tx)
		if err != nil {
			return err
		}
		if n >= MaxProfiles {
			return ErrLimit
		}
		return tx.Create(&p).Error
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Update writes the editable fields; ErrNotFound when the profile was deleted meanwhile.
// (Save would re-insert a deleted row and bypass MaxProfiles.)
func (s *Service) Update(p *Profile, in Input) error {
	if err := in.applyTo(p); err != nil {
		return err
	}
	res := s.db.Model(p).Select("name", "avatar", "color", "language", "updated_at").Updates(p)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a profile and all its per-profile data in one transaction,
// then tells cleaners that implement ProfileDeletedHook.
func (s *Service) Delete(id uint) error {
	if err := s.delete(id); err != nil {
		return err
	}
	for _, c := range s.cleaners {
		if h, ok := c.(ProfileDeletedHook); ok {
			h.ProfileDeleted(id)
		}
	}
	return nil
}

func (s *Service) delete(id uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		n, err := countProfiles(tx)
		if err != nil {
			return err
		}
		if n <= 1 {
			return ErrLastProfile
		}
		for _, c := range s.cleaners {
			if err := c.DeleteByProfile(tx, id); err != nil {
				return err
			}
		}
		return tx.Delete(&Profile{}, id).Error
	})
}

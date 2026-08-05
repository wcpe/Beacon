package service

import (
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// createAPIKeyForTest 仅供同包旧行为单测构造已签发密钥，不构成生产旁路。
func createAPIKeyForTest(s *APIKeyService, name, role string, expiresAt *time.Time, operator, clientIP string) (string, *model.APIKey, error) {
	var plaintext string
	var key *model.APIKey
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		plaintext, key, err = s.applyCreateInTx(tx, name, role, expiresAt, operator, clientIP)
		return err
	})
	return plaintext, key, err
}

// resetAPIKeyForTest 仅供同包旧行为单测轮换已签发密钥，不构成生产旁路。
func resetAPIKeyForTest(s *APIKeyService, id uint, operator, clientIP string) (string, *model.APIKey, error) {
	var plaintext string
	var key *model.APIKey
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		plaintext, key, err = s.applyResetInTx(tx, id, operator, clientIP)
		return err
	})
	return plaintext, key, err
}

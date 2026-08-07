package repository

import (
	"regexp"
	"testing"
)

func TestLegacyMessageTreeBackfillDoesNotUsePostgresCurrentUserKeywordAsAlias(t *testing.T) {
	reservedAlias := regexp.MustCompile(`(?i)\bAS\s+current_user\b`)
	if reservedAlias.MatchString(legacyMessageTreeBackfillSQL) {
		t.Fatal("legacy message tree backfill uses PostgreSQL CURRENT_USER keyword as an unquoted table alias")
	}
}

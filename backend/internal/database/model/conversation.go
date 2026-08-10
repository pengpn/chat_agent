package model

import "time"

// Conversation is the persisted conversation model.
type Conversation struct {
	ID                      string     `json:"id" gorm:"type:uuid;default:gen_random_uuid();primaryKey;index:conversations_user_updated_idx,priority:3,sort:desc"`
	UserID                  string     `json:"-" gorm:"type:uuid;not null;index:conversations_user_updated_idx,priority:1"`
	User                    *User      `json:"-" gorm:"foreignKey:UserID;references:ID;constraint:OnDelete:CASCADE"`
	Title                   string     `json:"title" gorm:"type:text;not null;default:New conversation"`
	TitleStatus             string     `json:"titleStatus" gorm:"type:text;not null;default:default;index;check:conversations_title_status_check,title_status IN ('default','generating','generated','fallback','manual')"`
	TitleProvider           string     `json:"-" gorm:"type:text;not null;default:''"`
	TitleModel              string     `json:"-" gorm:"type:text;not null;default:''"`
	TitleInputTokens        int64      `json:"-" gorm:"not null;default:0"`
	TitleOutputTokens       int64      `json:"-" gorm:"not null;default:0"`
	TitleTotalTokens        int64      `json:"-" gorm:"not null;default:0"`
	TitleUsageReported      bool       `json:"-" gorm:"not null;default:false"`
	TitleCostNanoUSD        int64      `json:"-" gorm:"not null;default:0"`
	TitleCostKnown          bool       `json:"-" gorm:"not null;default:false"`
	TitleGenerationStarted  *time.Time `json:"-"`
	TitleGeneratedAt        *time.Time `json:"-"`
	Summary                 string     `json:"-" gorm:"type:text;not null;default:''"`
	SummaryThroughMessageID *string    `json:"-" gorm:"type:uuid"`
	SummaryUpdatedAt        *time.Time `json:"-"`
	CreatedAt               time.Time  `json:"createdAt"`
	UpdatedAt               time.Time  `json:"updatedAt" gorm:"index:conversations_user_updated_idx,priority:2,sort:desc"`
}

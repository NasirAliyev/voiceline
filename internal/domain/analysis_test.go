package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewActionItem(t *testing.T) {
	tests := []struct {
		name    string
		task    string
		wantErr bool
	}{
		{"valid", "Send proposal", false},
		{"trims surrounding whitespace", "  Follow up with finance  ", false},
		{"blank task rejected", "   ", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item, err := NewActionItem(tt.task, "  Alice  ", "  Friday  ")
			if tt.wantErr {
				assert.ErrorIs(t, err, ErrInvalidArgument)
				return
			}
			require.NoError(t, err)
			assert.NotEmpty(t, item.Task)
			assert.NotContains(t, item.Task, "  ")
			assert.Equal(t, "Alice", item.Owner)
			assert.Equal(t, "Friday", item.Due)
		})
	}
}

func TestNewTranscript(t *testing.T) {
	t.Run("valid trims text", func(t *testing.T) {
		tr, err := NewTranscript("  hello world  ", "en")
		require.NoError(t, err)
		assert.Equal(t, "hello world", tr.Text)
		assert.Equal(t, "en", tr.Language)
	})

	t.Run("blank text rejected", func(t *testing.T) {
		_, err := NewTranscript("   ", "en")
		assert.ErrorIs(t, err, ErrInvalidArgument)
	})
}

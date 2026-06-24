// Package sheets delivers notes to Google Sheets by appending one row per
// voiceline. It satisfies domain.Destination; selecting it is a config switch
// (DESTINATION=sheets) with zero changes to core logic.
package sheets

import (
	"context"
	"fmt"

	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"

	"github.com/nasiraliev/voiceline/internal/domain"
)

const (
	// valueInputUserEntered makes Sheets parse values as if typed by a user
	// (so dates/numbers are interpreted rather than stored as raw strings).
	valueInputUserEntered = "USER_ENTERED"
	insertRows            = "INSERT_ROWS"
	appendCell            = "!A1" // append locates the table starting from here
)

// appender is the minimal Sheets capability Deliver needs. Hiding the SDK behind
// this one-method seam keeps Deliver unit-testable without network access.
type appender interface {
	Append(ctx context.Context, spreadsheetID, writeRange string, values []any) error
}

// Destination appends each delivered Voiceline as a row in a Google Sheet.
type Destination struct {
	appender      appender
	spreadsheetID string
	sheetName     string
}

// New builds a Sheets destination authenticated with a service account. When
// credentialsFile is empty it falls back to Application Default Credentials. The
// ctx is used only for client initialization.
func New(ctx context.Context, spreadsheetID, sheetName, credentialsFile string) (*Destination, error) {
	opts := []option.ClientOption{option.WithScopes(sheets.SpreadsheetsScope)}
	if credentialsFile != "" {
		opts = append(opts, option.WithCredentialsFile(credentialsFile))
	}
	svc, err := sheets.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("init sheets service: %w", err)
	}
	return &Destination{
		appender:      &apiAppender{svc: svc},
		spreadsheetID: spreadsheetID,
		sheetName:     sheetName,
	}, nil
}

// Deliver appends the note as a single row.
//
// The append is intentionally NOT retried: it is a non-idempotent mutation, so
// retrying after a partial failure could write a duplicate row. At-least-once
// delivery with possible duplicates is the documented trade-off (see README);
// idempotency keys would be the production follow-up.
func (d *Destination) Deliver(ctx context.Context, v domain.Voiceline) error {
	writeRange := d.sheetName + appendCell
	if err := d.appender.Append(ctx, d.spreadsheetID, writeRange, buildRow(v)); err != nil {
		return fmt.Errorf("append row to sheet %s: %w", d.spreadsheetID, err)
	}
	return nil
}

// apiAppender adapts the Sheets SDK to the appender seam.
type apiAppender struct {
	svc *sheets.Service
}

func (a *apiAppender) Append(ctx context.Context, spreadsheetID, writeRange string, values []any) error {
	body := &sheets.ValueRange{Values: [][]any{values}}
	_, err := a.svc.Spreadsheets.Values.
		Append(spreadsheetID, writeRange, body).
		ValueInputOption(valueInputUserEntered).
		InsertDataOption(insertRows).
		Context(ctx).
		Do()
	return err
}

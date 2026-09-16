package application

import (
	"context"
	"errors"
	"testing"
)

// recordingEmbed returns an embed closure (see embedTitleWeighted's embed
// parameter) that records every input it was called with, and returns a
// distinguishable vector per input so a test can assert exactly which
// text(s) reached it.
func recordingEmbed(calls *[]string) func(context.Context, string) ([]float32, error) {
	return func(_ context.Context, s string) ([]float32, error) {
		*calls = append(*calls, s)
		switch s {
		case "title":
			return []float32{1, 0}, nil
		case "body":
			return []float32{0, 1}, nil
		default:
			return []float32{9, 9}, nil
		}
	}
}

func TestEmbedTitleWeighted_EmptyTitleEmbedsBodyOnly(t *testing.T) {
	var calls []string
	vec, err := embedTitleWeighted(context.Background(), recordingEmbed(&calls), "", "body", 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 1 || calls[0] != "body" {
		t.Errorf("expected exactly one Embed call against the body, got %v", calls)
	}
	if vec[0] != 0 || vec[1] != 1 {
		t.Errorf("expected the body vector back, got %v", vec)
	}
}

func TestEmbedTitleWeighted_EmptyBodyEmbedsTitleOnly(t *testing.T) {
	var calls []string
	vec, err := embedTitleWeighted(context.Background(), recordingEmbed(&calls), "title", "", 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 1 || calls[0] != "title" {
		t.Errorf("expected exactly one Embed call against the title, got %v", calls)
	}
	if vec[0] != 1 || vec[1] != 0 {
		t.Errorf("expected the title vector back, got %v", vec)
	}
}

func TestEmbedTitleWeighted_WhitespaceOnlyTitleTreatedAsEmpty(t *testing.T) {
	var calls []string
	_, err := embedTitleWeighted(context.Background(), recordingEmbed(&calls), "   ", "body", 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 1 || calls[0] != "body" {
		t.Errorf("expected a whitespace-only title to be treated as empty, got %v", calls)
	}
}

func TestEmbedTitleWeighted_ZeroWeightEmbedsBodyOnlyEvenWithATitle(t *testing.T) {
	var calls []string
	_, err := embedTitleWeighted(context.Background(), recordingEmbed(&calls), "title", "body", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 1 || calls[0] != "body" {
		t.Errorf("expected titleWeight=0 to skip the title Embed call entirely, got %v", calls)
	}
}

func TestEmbedTitleWeighted_FullWeightEmbedsTitleOnlyEvenWithABody(t *testing.T) {
	var calls []string
	_, err := embedTitleWeighted(context.Background(), recordingEmbed(&calls), "title", "body", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 1 || calls[0] != "title" {
		t.Errorf("expected titleWeight=1 to skip the body Embed call entirely, got %v", calls)
	}
}

func TestEmbedTitleWeighted_InRangeWeightEmbedsBothAndCombines(t *testing.T) {
	var calls []string
	vec, err := embedTitleWeighted(context.Background(), recordingEmbed(&calls), "title", "body", 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 2 || calls[0] != "title" || calls[1] != "body" {
		t.Errorf("expected the title embedded before the body, got %v", calls)
	}
	if vec[0] != 0.5 || vec[1] != 0.5 {
		t.Errorf("expected the weighted midpoint of the title/body vectors, got %v", vec)
	}
}

func TestEmbedTitleWeighted_PropagatesTitleEmbedError(t *testing.T) {
	wantErr := errors.New("title embed failed")
	embed := func(_ context.Context, s string) ([]float32, error) {
		if s == "title" {
			return nil, wantErr
		}
		return []float32{0, 1}, nil
	}
	_, err := embedTitleWeighted(context.Background(), embed, "title", "body", 0.5)
	if !errors.Is(err, wantErr) {
		t.Errorf("expected the title embed's error to propagate, got %v", err)
	}
}

func TestEmbedTitleWeighted_PropagatesBodyEmbedError(t *testing.T) {
	wantErr := errors.New("body embed failed")
	embed := func(_ context.Context, s string) ([]float32, error) {
		if s == "body" {
			return nil, wantErr
		}
		return []float32{1, 0}, nil
	}
	_, err := embedTitleWeighted(context.Background(), embed, "title", "body", 0.5)
	if !errors.Is(err, wantErr) {
		t.Errorf("expected the body embed's error to propagate, got %v", err)
	}
}

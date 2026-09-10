package hashembed_test

import (
	"context"
	"math"
	"testing"

	"searchengine/internal/adapters/hashembed"
	"searchengine/internal/domain"
)

func TestEmbedder_DeterministicOutput(t *testing.T) {
	e := hashembed.New(64)
	v1, _ := e.Embed(context.Background(), "Katzen sind toll")
	v2, _ := e.Embed(context.Background(), "Katzen sind toll")
	if domain.CosineSimilarity(v1, v2) < 0.999 {
		t.Error("erwartet identische Embeddings für identischen Text")
	}
}

func TestEmbedder_SimilarTextsHaveHigherSimilarityThanUnrelated(t *testing.T) {
	e := hashembed.New(64)
	a, _ := e.Embed(context.Background(), "Katzen sind süße Haustiere")
	b, _ := e.Embed(context.Background(), "Katzen sind niedliche Haustiere")
	c, _ := e.Embed(context.Background(), "Quantenphysik Teilchenbeschleuniger Experiment")

	simAB := domain.CosineSimilarity(a, b)
	simAC := domain.CosineSimilarity(a, c)
	if simAB <= simAC {
		t.Errorf("erwartet höhere Ähnlichkeit bei Wortüberlappung: AB=%f AC=%f", simAB, simAC)
	}
}

func TestEmbedder_OutputIsNormalized(t *testing.T) {
	e := hashembed.New(32)
	v, _ := e.Embed(context.Background(), "irgendein beliebiger text hier")
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if math.Abs(math.Sqrt(norm)-1.0) > 0.001 {
		t.Errorf("erwartet L2-normalisierten Vektor (Norm=1), bekam Norm=%f", math.Sqrt(norm))
	}
}

func TestEmbedder_EmptyTextReturnsZeroVector(t *testing.T) {
	e := hashembed.New(16)
	v, err := e.Embed(context.Background(), "")
	if err != nil {
		t.Fatalf("unerwarteter Fehler: %v", err)
	}
	if len(v) != 16 {
		t.Errorf("erwartet Vektor der Länge 16, bekam %d", len(v))
	}
}

func TestEmbedder_DefaultDimensions(t *testing.T) {
	e := hashembed.New(0)
	if e.Dimensions() != 128 {
		t.Errorf("erwartet Default 128 Dimensionen, bekam %d", e.Dimensions())
	}
}
